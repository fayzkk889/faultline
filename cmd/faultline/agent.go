package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"time"

	"github.com/fayzkk889/faultline/internal/agent"
	"github.com/fayzkk889/faultline/internal/eval"
	"github.com/fayzkk889/faultline/internal/logs"
)

func cmdAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	data := fs.String("data", "data/dev", "split directory")
	out := fs.String("out", "", "write per-scenario results as JSON")
	traces := fs.String("traces", "", "directory for per-scenario transcripts")
	cfg := agent.Config{}
	fs.StringVar(&cfg.Endpoint, "endpoint", "http://localhost:11434", "Ollama endpoint")
	fs.StringVar(&cfg.Model, "model", "qwen3.5:9b", "model name")
	fs.IntVar(&cfg.NumCtx, "ctx", 16384, "context window")
	fs.IntVar(&cfg.MaxSteps, "steps", 14, "tool calls before forcing an answer")
	fs.IntVar(&cfg.Seed, "seed", 7, "sampling seed")
	fs.BoolVar(&cfg.Think, "think", false, "enable model thinking")
	timeout := fs.Duration("timeout", 5*time.Minute, "per model call")
	_ = fs.Parse(args)
	cfg.Timeout = *timeout
	return runEach(*data, *out, "agent "+cfg.Model, func(set *logs.Set) (eval.Diagnosis, error) {
		d, tr, err := agent.Diagnose(context.Background(), cfg, set)
		if *traces != "" && tr != nil {
			_ = os.MkdirAll(*traces, 0o755)
			b, _ := json.MarshalIndent(tr, "", "  ")
			_ = os.WriteFile(filepath.Join(*traces, filepath.Base(set.Dir)+".json"), b, 0o644)
		}
		return d, err
	})
}
