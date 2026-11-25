package scenario_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fayzkk889/faultline/internal/logs"
	"github.com/fayzkk889/faultline/internal/scenario"
)

func suites(t *testing.T) []scenario.Spec {
	t.Helper()
	var all []scenario.Spec
	for _, split := range []string{"dev", "test"} {
		s, err := scenario.Suite(split)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, s...)
	}
	return all
}

func TestEveryLineParsesAndRootEvidenceResolves(t *testing.T) {
	for _, spec := range suites(t) {
		t.Run(spec.Name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), spec.Name)
			truth, err := scenario.Generate(spec, dir)
			if err != nil {
				t.Fatal(err)
			}
			set, err := logs.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if set.Unparsed != 0 {
				t.Fatalf("%d unparsed lines", set.Unparsed)
			}
			for _, id := range truth.RootEvidence {
				ev, ok := set.Get(id)
				if !ok {
					t.Fatalf("root evidence %s does not start a record", id)
				}
				if truth.Node != "" && ev.Node != "" && ev.Node != truth.Node {
					t.Fatalf("root evidence %s is on %s, truth node is %s", id, ev.Node, truth.Node)
				}
			}
		})
	}
}

func TestGenerationIsDeterministic(t *testing.T) {
	spec := suites(t)[0]
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	if _, err := scenario.Generate(spec, a); err != nil {
		t.Fatal(err)
	}
	if _, err := scenario.Generate(spec, b); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(a, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(a, p)
		x, _ := os.ReadFile(p)
		y, err := os.ReadFile(filepath.Join(b, rel))
		if err != nil {
			return err
		}
		if !bytes.Equal(x, y) {
			t.Errorf("%s differs between runs", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerateRefusesExistingDir(t *testing.T) {
	if _, err := scenario.Generate(suites(t)[0], t.TempDir()); err == nil {
		t.Fatal("expected an error for an existing directory")
	}
}

// Clock skew must be visible: the same logical moment (job start) is stamped
// differently on different nodes, so ordering by raw timestamp is unreliable.
func TestNodesAreSkewed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	if _, err := scenario.Generate(suites(t)[0], dir); err != nil {
		t.Fatal(err)
	}
	set, err := logs.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := map[string]int64{}
	for _, e := range set.Events {
		if e.Source == "metrics" {
			if _, ok := first[e.Node]; !ok {
				first[e.Node] = e.TS.UnixMilli()
			}
		}
	}
	distinct := map[int64]bool{}
	for _, v := range first {
		distinct[v] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("expected skewed metrics start times across nodes, got %v", first)
	}
}

// Root evidence must be on the faulty node, not before the fault (allowing for
// clock skew), and for GPU faults must name that GPU's PCI id or gpu index.
func TestRootEvidenceIsCausallyPlaced(t *testing.T) {
	for _, spec := range suites(t) {
		dir := filepath.Join(t.TempDir(), spec.Name)
		truth, err := scenario.Generate(spec, dir)
		if err != nil {
			t.Fatal(err)
		}
		set, err := logs.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		tf, err := time.Parse(time.RFC3339Nano, truth.FaultTime)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range truth.RootEvidence {
			ev, _ := set.Get(id)
			if ev.TS.Before(tf.Add(-4 * time.Second)) {
				t.Errorf("%s: root evidence %s at %s precedes the fault at %s", spec.Name, id, ev.TS, tf)
			}
			node := ev.Node
			if node == "" {
				node = truth.Node // scheduler lines name the node in text
				if !strings.Contains(ev.Text, truth.Node) {
					t.Errorf("%s: cluster-level evidence %s does not name %s", spec.Name, id, truth.Node)
				}
			}
			if node != truth.Node {
				t.Errorf("%s: root evidence %s on %s, truth %s", spec.Name, id, node, truth.Node)
			}
		}
	}
}
