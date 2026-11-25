package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTracebackFoldsIntoItsRecord(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "node01/train.log", strings.Join([]string{
		"2026-03-14 10:00:00,000 [rank 0] INFO step 50 loss 3.0",
		"2026-03-14 10:00:01,000 [rank 0] ERROR Checkpoint save failed",
		"Traceback (most recent call last):",
		"OSError: [Errno 28] No space left on device",
		"2026-03-14 10:00:02,000 [rank 1] INFO step 100 loss 2.9",
	}, "\n")+"\n")
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Events) != 3 || s.Unparsed != 0 {
		t.Fatalf("events=%d unparsed=%d", len(s.Events), s.Unparsed)
	}
	e, ok := s.Get("node01/train.log:2")
	if !ok || !strings.Contains(e.Text, "Errno 28") || e.Level != Error {
		t.Fatalf("folded record = %+v", e)
	}
	if _, ok := s.Get("node01/train.log:3"); ok {
		t.Fatal("a continuation line must not start a record")
	}
	if e, _ := s.Get("node01/train.log:5"); e.Node != "node01" || e.Line != 5 {
		t.Fatalf("line numbering after a traceback is off: %+v", e)
	}
}

func TestKernelPriorityDrivesLevel(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "node02/kern.log", strings.Join([]string{
		"2026-03-14T10:00:00.000000+00:00 node02 kern.err kernel: [  10.000000] NVRM: Xid (PCI:0000:18:00): 79, pid=1, name=python3, GPU has fallen off the bus.",
		"2026-03-14T10:00:01.000000+00:00 node02 kern.info kernel: [  11.000000] mlx5_core 0000:0c:00.0 ibp12s0: Link up",
		"2026-03-14T10:00:02.000000+00:00 node02 kern.info kernel: [  12.000000] mlx5_core 0000:0c:00.0 ibp12s0: Link down",
		"not a kernel line",
	}, "\n")+"\n")
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Level{Error, Info, Warn}
	for i, w := range want {
		if s.Events[i].Level != w {
			t.Errorf("record %d level %v, want %v", i, s.Events[i].Level, w)
		}
	}
	if s.Unparsed != 1 {
		t.Fatalf("unparsed = %d, want 1", s.Unparsed)
	}
}

func TestSlowdownInsideIdentifierIsAWarning(t *testing.T) {
	if inferLevel("clocks_event=hw_slowdown,sw_thermal") != Warn {
		t.Fatal("hw_slowdown should infer a warning")
	}
	if inferLevel("clocks_event=none") != Info {
		t.Fatal("a healthy sample should be info")
	}
}

func TestTruthFileIsNeverLoaded(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "truth.json", `{"node":"node01"}`)
	write(t, dir, "scheduler.log", "[2026-03-14T10:00:00.000] job 1 step 0 started ntasks=32\n")
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range s.Events {
		if strings.Contains(e.File, "truth") {
			t.Fatal("truth.json was parsed")
		}
	}
}
