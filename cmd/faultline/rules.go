package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/fayzkk889/faultline/internal/eval"
	"github.com/fayzkk889/faultline/internal/logs"
	"github.com/fayzkk889/faultline/internal/rules"
)

func cmdRules(args []string) error {
	fs := flag.NewFlagSet("rules", flag.ExitOnError)
	data := fs.String("data", "data/dev", "split directory containing scenario directories")
	out := fs.String("out", "", "write per-scenario results as JSON")
	_ = fs.Parse(args)
	return runEach(*data, *out, "rules", func(set *logs.Set) (eval.Diagnosis, error) {
		return rules.Diagnose(set), nil
	})
}

// runEach applies diagnose to every scenario under data, scores it and prints a table.
func runEach(data, out, name string, diagnose func(*logs.Set) (eval.Diagnosis, error)) error {
	dirs, err := scenarioDirs(data)
	if err != nil {
		return err
	}
	var results []eval.Result
	for _, dir := range dirs {
		truth, err := eval.LoadTruth(dir)
		if err != nil {
			return err
		}
		set, err := logs.Load(dir)
		if err != nil {
			return err
		}
		d, derr := diagnose(set)
		r := eval.Score(truth, d, set)
		if derr != nil {
			r.Error = derr.Error()
			r.Full = false
		}
		results = append(results, r)
		fmt.Fprintf(os.Stderr, "%-40s full=%v predicted=%s/%s/%d\n", truth.Name, r.Full, d.Fault, d.Node, d.GPU)
	}
	eval.WriteTable(os.Stdout, name+" on "+filepath.ToSlash(data), results)
	if out != "" {
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, append(b, '\n'), 0o644)
	}
	return nil
}

func scenarioDirs(data string) ([]string, error) {
	entries, err := os.ReadDir(data)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(data, e.Name()))
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}
