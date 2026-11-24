package eval

import (
	"path/filepath"
	"testing"

	"github.com/fayzkk889/faultline/internal/logs"
	"github.com/fayzkk889/faultline/internal/scenario"
)

func fixture(t *testing.T) (scenario.Truth, *logs.Set) {
	t.Helper()
	specs, err := scenario.Suite("dev")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "s")
	truth, err := scenario.Generate(specs[3], dir) // gpu_ecc_uncorrectable
	if err != nil {
		t.Fatal(err)
	}
	set, err := logs.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return truth, set
}

func TestFullRequiresGroundedCitation(t *testing.T) {
	truth, set := fixture(t)
	// Right answer, but citing an existing non-root line (the scheduler job start).
	r := Score(truth, Diagnosis{Fault: truth.Expected, Node: truth.Node, GPU: truth.GPU, Evidence: []string{"scheduler.log:1"}}, set)
	if !r.Type || !r.Node || !r.GPU || r.Grounded || r.Full {
		t.Fatalf("%+v", r)
	}
	r = Score(truth, Diagnosis{Fault: truth.Expected, Node: truth.Node, GPU: truth.GPU, Evidence: []string{"scheduler.log:1", truth.RootEvidence[0]}}, set)
	if !r.Full {
		t.Fatalf("one root citation should suffice: %+v", r)
	}
}

func TestInvalidCitationsAreRecordedNotGrounded(t *testing.T) {
	truth, set := fixture(t)
	r := Score(truth, Diagnosis{Fault: truth.Expected, Node: truth.Node, GPU: truth.GPU, Evidence: []string{"node01/kern.log:999999"}}, set)
	if r.Grounded || len(r.InvalidCitations) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestWrongGPUIsNotFull(t *testing.T) {
	truth, set := fixture(t)
	r := Score(truth, Diagnosis{Fault: truth.Expected, Node: truth.Node, GPU: (truth.GPU + 1) % 8, Evidence: truth.RootEvidence[:1]}, set)
	if r.GPU || r.Full {
		t.Fatalf("%+v", r)
	}
}
