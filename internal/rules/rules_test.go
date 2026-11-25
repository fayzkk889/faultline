package rules

import (
	"path/filepath"
	"testing"

	"github.com/fayzkk889/faultline/internal/eval"
	"github.com/fayzkk889/faultline/internal/logs"
	"github.com/fayzkk889/faultline/internal/scenario"
)

// These pin the recorded baseline numbers so an accidental edit to the frozen
// rules shows up as a failing test rather than a silently different table.
func TestRecordedBaselineNumbers(t *testing.T) {
	cases := []struct {
		split string
		full  bool
		want  int
	}{
		{"dev", false, 18},
		{"test", false, 0},
		{"dev", true, 18},
		{"test", true, 22},
	}
	for _, c := range cases {
		specs, err := scenario.Suite(c.split)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for _, s := range specs {
			dir := filepath.Join(t.TempDir(), s.Name)
			truth, err := scenario.Generate(s, dir)
			if err != nil {
				t.Fatal(err)
			}
			set, err := logs.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if eval.Score(truth, Diagnose(set, Options{Full: c.full}), set).Full {
				got++
			}
		}
		if got != c.want {
			t.Errorf("%s full=%v: %d full-correct, recorded %d", c.split, c.full, got, c.want)
		}
	}
}

func TestGenericBaselineNumbers(t *testing.T) {
	for split, want := range map[string][2]int{"dev": {0, 12}, "test": {4, 19}} {
		specs, err := scenario.Suite(split)
		if err != nil {
			t.Fatal(err)
		}
		full, node := 0, 0
		for _, s := range specs {
			dir := filepath.Join(t.TempDir(), s.Name)
			truth, err := scenario.Generate(s, dir)
			if err != nil {
				t.Fatal(err)
			}
			set, err := logs.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			r := eval.Score(truth, Generic(set), set)
			if r.Full {
				full++
			}
			if r.Node {
				node++
			}
		}
		if full != want[0] || node != want[1] {
			t.Errorf("%s: full=%d node=%d, recorded %v", split, full, node, want)
		}
	}
}
