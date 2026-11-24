// Package agent runs a tool-using diagnosis loop against a local Ollama model.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fayzkk889/faultline/internal/eval"
	"github.com/fayzkk889/faultline/internal/logs"
	"github.com/fayzkk889/faultline/internal/scenario"
)

// Config controls one agent run.
type Config struct {
	Endpoint string // e.g. http://localhost:11434
	Model    string
	NumCtx   int
	MaxSteps int // tool calls before the agent is forced to answer
	Seed     int
	Think    bool
	Timeout  time.Duration // per model call
}

// Message is an Ollama chat message.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`
}

// ToolCall is a model-requested call.
type ToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// Trace records a run for inspection.
type Trace struct {
	Messages     []Message `json:"messages"`
	ToolCalls    int       `json:"tool_calls"`
	Rejected     int       `json:"rejected_answers"`
	PromptTokens int       `json:"prompt_tokens"`
	OutputTokens int       `json:"output_tokens"`
	WallSeconds  float64   `json:"wall_seconds"`
	ForcedAnswer bool      `json:"forced_answer"`
}

const systemPrompt = `You are an on-call infrastructure engineer diagnosing why a multi-node GPU training job failed or degraded.
You can only see the logs through the tools. Work like a careful engineer:
- Collective-operation timeouts, peer errors and job failures are usually SYMPTOMS that appear on every rank. Look for the earliest hardware, host or storage event that explains them, on one specific node.
- Ignore events from before the job started and conditions that recovered on their own.
- Per-node clocks can be skewed by a few seconds, so do not decide only by which timestamp is earliest across nodes.
- Kernel lines name GPUs by PCI bus id; map them to a GPU index with the node's hw.txt (search for the bus id).
- A missing signal can be evidence too (for example a GPU that stops reporting metrics).

Answer by calling diagnose exactly once, with:
- fault: one of gpu_fell_off_bus, gpu_ecc_uncorrectable, ib_link_down, host_oom, gpu_thermal_slowdown, storage_full, or other (a real root fault that is none of these).
- node: e.g. node03.
- gpu: the GPU index on that node, or -1 if the fault is not a single GPU (network, host memory, storage).
- evidence: ids (like node03/kern.log:812) of the log records that directly show the root fault, copied exactly from tool output. Cite the root-cause records, not the timeouts.
- explanation: two or three sentences.`

func toolSpecs() []map[string]any {
	fn := func(name, desc string, props map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "function", "function": map[string]any{
			"name": name, "description": desc,
			"parameters": map[string]any{"type": "object", "properties": props, "required": required},
		}}
	}
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	num := func(d string) map[string]any { return map[string]any{"type": "integer", "description": d} }
	return []map[string]any{
		fn("overview", "List log files with record counts, warning/error counts and time ranges, plus every scheduler event.", map[string]any{}),
		fn("anomalies", "Warning-or-worse records clustered by message template (numbers masked), ordered by first occurrence, with counts, first/last ids and nodes.", map[string]any{}),
		fn("metrics_summary", "Per-GPU summary of metrics.log samples: sample count, last sample, and each field's range or distinct values. Flags GPUs whose samples stopped early.", map[string]any{}),
		fn("search", "Case-insensitive regex search over records. Returns ids, times and text (max 30).", map[string]any{
			"pattern": str("RE2 regular expression"),
			"file":    str("optional: a file like node02/kern.log, or a node directory like node02"),
			"limit":   num("max results, up to 30"),
		}, "pattern"),
		fn("context", "Show records before and after an id in the same file.", map[string]any{
			"id": str("record id, e.g. node02/kern.log:120"), "before": num("records before (max 15)"), "after": num("records after (max 15)"),
		}, "id"),
		fn("diagnose", "Submit the final diagnosis.", map[string]any{
			"fault":       map[string]any{"type": "string", "enum": append(append([]string{}, scenario.Known...), scenario.Other)},
			"node":        str("node name"),
			"gpu":         num("GPU index on the node, or -1"),
			"evidence":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "record ids that directly show the root fault"},
			"explanation": str("two or three sentences"),
		}, "fault", "node", "gpu", "evidence", "explanation"),
	}
}

type chatRequest struct {
	Model    string           `json:"model"`
	Messages []Message        `json:"messages"`
	Tools    []map[string]any `json:"tools"`
	Stream   bool             `json:"stream"`
	Think    bool             `json:"think"`
	Options  map[string]any   `json:"options"`
}

type chatResponse struct {
	Message         Message `json:"message"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
	Error           string  `json:"error"`
}

func (c Config) chat(ctx context.Context, msgs []Message) (chatResponse, error) {
	body, err := json.Marshal(chatRequest{
		Model: c.Model, Messages: msgs, Tools: toolSpecs(), Think: c.Think,
		Options: map[string]any{"temperature": 0, "seed": c.Seed, "num_ctx": c.NumCtx},
	})
	if err != nil {
		return chatResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.Endpoint, "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return chatResponse{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return chatResponse{}, err
	}
	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return chatResponse{}, fmt.Errorf("decode %d response: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || out.Error != "" {
		return chatResponse{}, fmt.Errorf("ollama %d: %s", resp.StatusCode, out.Error)
	}
	return out, nil
}

const maxRejected = 2

// Diagnose runs the loop on one scenario.
func Diagnose(ctx context.Context, cfg Config, set *logs.Set) (eval.Diagnosis, *Trace, error) {
	tools := &Tools{set: set}
	tr := &Trace{}
	start := time.Now()
	defer func() { tr.WallSeconds = time.Since(start).Seconds() }()
	msgs := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "The job's logs are loaded. Find the root cause. Start with overview or anomalies."},
	}
	for round := 0; ; round++ {
		if tr.ToolCalls >= cfg.MaxSteps && !tr.ForcedAnswer {
			tr.ForcedAnswer = true
			msgs = append(msgs, Message{Role: "user", Content: "You have used your investigation budget. Call diagnose now with your best answer."})
		}
		if round > cfg.MaxSteps+6 {
			tr.Messages = msgs
			return eval.Diagnosis{}, tr, errors.New("no diagnosis within the round limit")
		}
		resp, err := cfg.chat(ctx, msgs)
		if err != nil {
			tr.Messages = msgs
			return eval.Diagnosis{}, tr, err
		}
		tr.PromptTokens += resp.PromptEvalCount
		tr.OutputTokens += resp.EvalCount
		m := resp.Message
		m.Role = "assistant"
		msgs = append(msgs, m)
		if len(m.ToolCalls) == 0 {
			msgs = append(msgs, Message{Role: "user", Content: "Use the tools to investigate, then call diagnose."})
			continue
		}
		for _, call := range m.ToolCalls {
			name := call.Function.Name
			if name == "diagnose" {
				d, problem := parseDiagnosis(call.Function.Arguments, set)
				if problem == "" {
					tr.Messages = append(msgs, Message{Role: "tool", ToolName: name, Content: "accepted"})
					return d, tr, nil
				}
				tr.Rejected++
				if tr.Rejected > maxRejected {
					tr.Messages = msgs
					return d, tr, fmt.Errorf("diagnosis rejected %d times: %s", tr.Rejected, problem)
				}
				msgs = append(msgs, Message{Role: "tool", ToolName: name, Content: "rejected: " + problem + " Fix it and call diagnose again."})
				continue
			}
			tr.ToolCalls++
			msgs = append(msgs, Message{Role: "tool", ToolName: name, Content: runTool(tools, name, call.Function.Arguments)})
		}
	}
}

func runTool(t *Tools, name string, raw json.RawMessage) string {
	var a struct {
		Pattern string  `json:"pattern"`
		File    string  `json:"file"`
		Limit   flexInt `json:"limit"`
		ID      string  `json:"id"`
		Before  flexInt `json:"before"`
		After   flexInt `json:"after"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "bad arguments: " + err.Error()
		}
	}
	switch name {
	case "overview":
		return t.Overview()
	case "anomalies":
		return t.Anomalies()
	case "metrics_summary":
		return t.MetricsSummary()
	case "search":
		if a.Pattern == "" {
			return "pattern is required"
		}
		return t.Search(a.Pattern, a.File, int(a.Limit))
	case "context":
		b, f := int(a.Before), int(a.After)
		if b == 0 && f == 0 {
			b, f = 5, 5
		}
		return t.Context(a.ID, b, f)
	}
	return "unknown tool " + name
}

// flexInt accepts 5 or "5"; small models emit both.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	var n int
	if _, err := fmt.Sscan(s, &n); err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

func parseDiagnosis(raw json.RawMessage, set *logs.Set) (eval.Diagnosis, string) {
	var a struct {
		Fault       string          `json:"fault"`
		Node        string          `json:"node"`
		GPU         flexInt         `json:"gpu"`
		Evidence    json.RawMessage `json:"evidence"`
		Explanation string          `json:"explanation"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return eval.Diagnosis{}, "arguments are not valid JSON: " + err.Error() + "."
	}
	d := eval.Diagnosis{Fault: a.Fault, Node: a.Node, GPU: int(a.GPU), Explanation: a.Explanation}
	// Some models send the array as a JSON-encoded string.
	if err := json.Unmarshal(a.Evidence, &d.Evidence); err != nil {
		var s string
		if json.Unmarshal(a.Evidence, &s) == nil {
			_ = json.Unmarshal([]byte(s), &d.Evidence)
			if len(d.Evidence) == 0 && s != "" {
				d.Evidence = []string{s}
			}
		}
	}
	if !scenario.IsKnown(d.Fault) && d.Fault != scenario.Other {
		return d, fmt.Sprintf("fault %q is not an allowed value.", d.Fault)
	}
	if len(d.Evidence) == 0 {
		return d, "evidence is empty."
	}
	var missing []string
	for _, id := range d.Evidence {
		if _, ok := set.Get(id); !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return d, fmt.Sprintf("these evidence ids do not exist: %s. Copy ids exactly from tool output.", strings.Join(missing, ", "))
	}
	return d, ""
}
