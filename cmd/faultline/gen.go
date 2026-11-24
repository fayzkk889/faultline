package main

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/fayzkk889/faultline/internal/scenario"
)

func cmdGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	split := fs.String("split", "dev", "dev or test")
	out := fs.String("out", "data", "output root; scenarios go to OUT/SPLIT/NAME")
	_ = fs.Parse(args)
	specs, err := scenario.Suite(*split)
	if err != nil {
		return err
	}
	for _, s := range specs {
		t, err := scenario.Generate(s, filepath.Join(*out, *split, s.Name))
		if err != nil {
			return err
		}
		fmt.Printf("%-40s %-22s %s gpu=%d root=%d\n", s.Name, t.Expected, t.Node, t.GPU, len(t.RootEvidence))
	}
	return nil
}
