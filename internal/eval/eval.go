// Package eval scores diagnoses against ground truth. The rules here are the
// ones frozen in DESIGN.md; changing them invalidates recorded results.
package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fayzkk889/faultline/internal/logs"
	"github.com/fayzkk889/faultline/internal/scenario"
)

// Diagnosis is the output contract shared by every diagnoser.
type Diagnosis struct {
	Fault       string   `json:"fault"`
	Node        string   `json:"node"`
	GPU         int      `json:"gpu"`
	Evidence    []string `json:"evidence"`
	Explanation string   `json:"explanation"`
}

// Result is the score of one diagnosis.
type Result struct {
	Scenario string `json:"scenario"`
	Split    string `json:"split"`
	Fault    string `json:"fault"`
	Variant  string `json:"variant"`
	Expected string `json:"expected"`

	Predicted Diagnosis `json:"predicted"`

	Type     bool `json:"type"`
	Node     bool `json:"node"`
	GPU      bool `json:"gpu"`
	Grounded bool `json:"grounded"`
	Full     bool `json:"full"`

	// Precision is the share of cited ids that are root evidence. Reported, not
	// part of Full: it shows whether grounding came from a few precise citations.
	Precision float64 `json:"citation_precision"`

	InvalidCitations []string `json:"invalid_citations,omitempty"`
	Error            string   `json:"error,omitempty"`
}

// LoadTruth reads truth.json from a scenario directory.
func LoadTruth(dir string) (scenario.Truth, error) {
	var t scenario.Truth
	b, err := os.ReadFile(filepath.Join(dir, "truth.json"))
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(b, &t)
}

// Score compares d to t. set is used only to check that citations exist.
func Score(t scenario.Truth, d Diagnosis, set *logs.Set) Result {
	r := Result{Scenario: t.Name, Split: t.Split, Fault: t.Fault, Variant: t.Variant, Expected: t.Expected, Predicted: d}
	r.Type = d.Fault == t.Expected
	r.Node = d.Node == t.Node
	r.GPU = d.GPU == t.GPU
	root := map[string]bool{}
	for _, id := range t.RootEvidence {
		root[id] = true
	}
	for _, id := range d.Evidence {
		if _, ok := set.Get(id); !ok {
			r.InvalidCitations = append(r.InvalidCitations, id)
			continue
		}
		if root[id] {
			r.Grounded = true
		}
	}
	hits := 0
	for _, id := range d.Evidence {
		if root[id] {
			hits++
		}
	}
	if len(d.Evidence) > 0 {
		r.Precision = float64(hits) / float64(len(d.Evidence))
	}
	r.Full = r.Type && r.Node && r.GPU && r.Grounded
	return r
}

// Summary aggregates results by split.
type Summary struct {
	Split            string
	N                int
	Type, Node, GPU  int
	Grounded, Full   int
	PrecisionSum     float64
	InvalidCitations int
	Errors           int
}

// Summarize groups results by split, sorted by split name.
func Summarize(rs []Result) []Summary {
	by := map[string]*Summary{}
	for _, r := range rs {
		s := by[r.Split]
		if s == nil {
			s = &Summary{Split: r.Split}
			by[r.Split] = s
		}
		s.N++
		s.Type += b2i(r.Type)
		s.Node += b2i(r.Node)
		s.GPU += b2i(r.GPU)
		s.Grounded += b2i(r.Grounded)
		s.Full += b2i(r.Full)
		s.PrecisionSum += r.Precision
		s.InvalidCitations += len(r.InvalidCitations)
		if r.Error != "" {
			s.Errors++
		}
	}
	var out []Summary
	for _, s := range by {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Split < out[j].Split })
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// WriteTable prints a markdown summary and a per-scenario table.
func WriteTable(w io.Writer, name string, rs []Result) {
	fmt.Fprintf(w, "### %s\n\n", name)
	fmt.Fprintln(w, "| split | n | full | type | node | gpu | grounded | mean citation precision | invalid citations | errors |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|")
	for _, s := range Summarize(rs) {
		fmt.Fprintf(w, "| %s | %d | %d/%d | %d | %d | %d | %d | %.2f | %d | %d |\n", s.Split, s.N, s.Full, s.N, s.Type, s.Node, s.GPU, s.Grounded, s.PrecisionSum/float64(s.N), s.InvalidCitations, s.Errors)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| scenario | expected | predicted | node | gpu | grounded | full |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
	for _, r := range rs {
		pred := r.Predicted.Fault
		if r.Error != "" {
			pred = "error: " + strings.ReplaceAll(r.Error, "|", "/")
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s |\n", r.Scenario, r.Expected, pred, mark(r.Node), mark(r.GPU), mark(r.Grounded), mark(r.Full))
	}
	fmt.Fprintln(w)
}

func mark(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
