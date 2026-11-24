package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fayzkk889/faultline/internal/logs"
	"github.com/fayzkk889/faultline/internal/scenario"
)

func loadDev(t *testing.T, i int) (*logs.Set, scenario.Truth) {
	t.Helper()
	specs, err := scenario.Suite("dev")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "s")
	truth, err := scenario.Generate(specs[i], dir)
	if err != nil {
		t.Fatal(err)
	}
	set, err := logs.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return set, truth
}

// scripted is a fake Ollama /api/chat that replays tool calls and records requests.
type scripted struct {
	mu       sync.Mutex
	replies  []string // raw JSON "arguments" per turn, as "name|args"
	requests []chatRequest
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	if len(s.replies) == 0 {
		http.Error(w, `{"error":"script exhausted"}`, 500)
		return
	}
	name, args, _ := strings.Cut(s.replies[0], "|")
	s.replies = s.replies[1:]
	resp := map[string]any{
		"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"function": map[string]any{"name": name, "arguments": json.RawMessage(args)}},
		}},
		"prompt_eval_count": 10, "eval_count": 5,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func cfgFor(url string) Config {
	return Config{Endpoint: url, Model: "fake", NumCtx: 4096, MaxSteps: 5, Seed: 1, Timeout: 10 * time.Second}
}

func TestLoopRejectsInventedEvidenceThenAccepts(t *testing.T) {
	set, truth := loadDev(t, 0)
	good, _ := json.Marshal(map[string]any{"fault": truth.Expected, "node": truth.Node, "gpu": truth.GPU, "evidence": truth.RootEvidence[:1], "explanation": "x"})
	fake := &scripted{replies: []string{
		`overview|{}`,
		`diagnose|{"fault":"gpu_fell_off_bus","node":"node01","gpu":1,"evidence":["node09/kern.log:1"],"explanation":"x"}`,
		"diagnose|" + string(good),
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	d, tr, err := Diagnose(context.Background(), cfgFor(srv.URL), set)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Rejected != 1 || tr.ToolCalls != 1 {
		t.Fatalf("rejected=%d toolcalls=%d, want 1 and 1", tr.Rejected, tr.ToolCalls)
	}
	if d.Node != truth.Node || d.Evidence[0] != truth.RootEvidence[0] {
		t.Fatalf("accepted %+v", d)
	}
	// The model must have been told which id was invented.
	last := fake.requests[2].Messages
	if msg := last[len(last)-1].Content; !strings.Contains(msg, "node09/kern.log:1") || !strings.HasPrefix(msg, "rejected") {
		t.Fatalf("rejection message = %q", msg)
	}
}

func TestLoopGivesUpAfterRepeatedInvalidAnswers(t *testing.T) {
	set, _ := loadDev(t, 0)
	bad := `diagnose|{"fault":"made_up","node":"node01","gpu":0,"evidence":["x"],"explanation":"x"}`
	srv := httptest.NewServer(&scripted{replies: []string{bad, bad, bad}})
	defer srv.Close()
	if _, tr, err := Diagnose(context.Background(), cfgFor(srv.URL), set); err == nil || tr.Rejected != 3 {
		t.Fatalf("err=%v rejected=%d, want an error after 3 rejections", err, tr.Rejected)
	}
}

func TestLoopForcesAnswerAfterBudget(t *testing.T) {
	set, truth := loadDev(t, 0)
	var replies []string
	for i := 0; i < 5; i++ {
		replies = append(replies, `search|{"pattern":"Xid","limit":"3"}`)
	}
	good, _ := json.Marshal(map[string]any{"fault": truth.Expected, "node": truth.Node, "gpu": truth.GPU, "evidence": truth.RootEvidence, "explanation": "x"})
	replies = append(replies, "diagnose|"+string(good))
	fake := &scripted{replies: replies}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, tr, err := Diagnose(context.Background(), cfgFor(srv.URL), set)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.ForcedAnswer {
		t.Fatal("expected the budget message to be sent")
	}
	final := fake.requests[len(fake.requests)-1]
	msgs := final.Messages
	if !strings.Contains(msgs[len(msgs)-1].Content, "investigation budget") {
		t.Fatalf("last message before answer = %q", msgs[len(msgs)-1].Content)
	}
	if len(final.Tools) != 1 {
		t.Fatalf("forced turn offered %d tools, want only diagnose", len(final.Tools))
	}
	// Older tool outputs are shortened; the newest are sent verbatim.
	var tools []Message
	for _, m := range msgs {
		if m.Role == "tool" {
			tools = append(tools, m)
		}
	}
	if !strings.Contains(tools[0].Content, "older output shortened") && len(tools[0].Content) > shortenedHead {
		t.Fatal("oldest tool output was not compacted")
	}
	if strings.Contains(tools[len(tools)-1].Content, "older output shortened") {
		t.Fatal("newest tool output was compacted")
	}
}

func TestCompactKeepsSystemPromptAndDoesNotMutateHistory(t *testing.T) {
	long := strings.Repeat("x", 2000)
	msgs := []Message{{Role: "system", Content: systemPrompt}}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, Message{Role: "tool", Content: long})
	}
	out := compact(msgs)
	if out[0].Content != systemPrompt {
		t.Fatal("system prompt changed")
	}
	if len(out[1].Content) >= len(long) || len(out[6].Content) != len(long) {
		t.Fatal("wrong messages compacted")
	}
	if len(msgs[1].Content) != len(long) {
		t.Fatal("compact mutated the caller's history")
	}
}

func TestParseDiagnosisAcceptsStringEncodedEvidence(t *testing.T) {
	set, truth := loadDev(t, 0)
	enc, _ := json.Marshal(truth.RootEvidence[:1])
	raw, _ := json.Marshal(map[string]any{"fault": truth.Expected, "node": truth.Node, "gpu": "5", "evidence": string(enc), "explanation": "x"})
	d, problem := parseDiagnosis(raw, set)
	if problem != "" {
		t.Fatal(problem)
	}
	if len(d.Evidence) != 1 || d.Evidence[0] != truth.RootEvidence[0] || d.GPU != 5 {
		t.Fatalf("%+v", d)
	}
}

func TestAnomaliesMarksPreJobEvents(t *testing.T) {
	set, _ := loadDev(t, 0)
	out := (&Tools{set: set}).Anomalies()
	if !strings.Contains(out, "job start (scheduler):") {
		t.Fatal("missing job start header")
	}
	// Every scenario has a benign Xid 13 from a previous job.
	if !strings.Contains(out, "BEFORE job start") {
		t.Fatalf("pre-job events not marked:\n%s", out)
	}
}

func TestMetricsSummaryFlagsStoppedGPU(t *testing.T) {
	specs, _ := scenario.Suite("test")
	var spec scenario.Spec
	for _, s := range specs {
		if s.Fault == "gpu_fell_off_bus" {
			spec = s
			break
		}
	}
	dir := filepath.Join(t.TempDir(), "s")
	truth, err := scenario.Generate(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	set, err := logs.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := (&Tools{set: set}).MetricsSummary()
	var flagged []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "STOPPED") {
			flagged = append(flagged, strings.Fields(line)[0])
		}
	}
	want := truth.Node + "/gpu" + string(rune('0'+truth.GPU))
	if len(flagged) != 1 || flagged[0] != want {
		t.Fatalf("flagged %v, want [%s]", flagged, want)
	}
}

func TestSearchLastReturnsLatestMatches(t *testing.T) {
	set, _ := loadDev(t, 0)
	tl := &Tools{set: set}
	first := tl.Search(`step [0-9]+ loss`, "node01/train.log", 2, false)
	last := tl.Search(`step [0-9]+ loss`, "node01/train.log", 2, true)
	if first == last || !strings.Contains(first, "set last=true") || !strings.Contains(last, "showing last 2") {
		t.Fatalf("first:\n%s\nlast:\n%s", first, last)
	}
}

func TestAnomaliesSeparatesPreJobTemplates(t *testing.T) {
	set, _ := loadDev(t, 0)
	out := (&Tools{set: set}).Anomalies()
	during := strings.Index(out, "DURING the job")
	before := strings.Index(out, "BEFORE the job started")
	xid13 := strings.Index(out, "): 13,")
	if during < 0 || before < 0 || xid13 < before {
		t.Fatalf("the pre-job Xid 13 should be listed in the BEFORE section:\n%s", out)
	}
}

func TestMetricsSummaryCitesFirstThrottledSample(t *testing.T) {
	specs, _ := scenario.Suite("dev")
	var spec scenario.Spec
	for _, s := range specs {
		if s.Fault == "gpu_thermal_slowdown" {
			spec = s
			break
		}
	}
	dir := filepath.Join(t.TempDir(), "s")
	truth, err := scenario.Generate(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	set, err := logs.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := (&Tools{set: set}).MetricsSummary()
	root := map[string]bool{}
	for _, id := range truth.RootEvidence {
		root[id] = true
	}
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "hw_slowdown,sw_thermal first@"); i >= 0 {
			id := strings.Fields(strings.TrimRight(line[i+len("hw_slowdown,sw_thermal first@"):], "}"))[0]
			id = strings.TrimSuffix(strings.TrimSuffix(id, "}"), ";")
			if !root[id] {
				t.Fatalf("cited %s is not root evidence", id)
			}
			return
		}
	}
	t.Fatalf("no throttled first@ id in:\n%s", out)
}
