# faultline

A reproducible test bed for one question: **when a multi-node GPU training job fails, can a
diagnoser name the root fault, on the right node and GPU, and cite a log line that actually shows
it?**

When a distributed job dies, nearly every rank reports the same collective timeout. The useful
signal is usually one kernel line, a metrics exporter that stopped reading a GPU, or a scheduler
note, minutes earlier, on one host, among benign noise and clock skew. faultline generates that
situation with known ground truth, and scores three diagnosers on it:

- `rules`: a signature baseline written from the dev split only, and frozen in git before the test
  split existed;
- `rules-full`: the same rules plus signatures written *with* knowledge of the test phrasings and the
  public Xid catalog, an informed upper bound;
- `agent`: a tool-using loop on a local model (Ollama) that must cite record ids, which the harness
  verifies exist.

Results: [RESULTS.md](RESULTS.md). Design and scoring contract: [DESIGN.md](DESIGN.md). Every agent
change and its dev score: [ITERATIONS.md](ITERATIONS.md).

All logs are synthetic. Formats are modeled on public conventions (kernel NVRM/Xid and mlx5 lines,
Python logging tracebacks, Slurm controller messages, logfmt GPU samples). Nothing here is real
production data or any company's internal format.

## What a scenario looks like

```
data/test/test-gpu_fell_off_bus-0/
  scheduler.log            job start/end, cgroup OOM notes
  node01..04/kern.log      kernel lines with syslog priority (NVRM, mlx5, oom-killer, noise)
  node01..04/train.log     32 ranks: steps, NCCL warnings, tracebacks, watchdog timeouts
  node01..04/metrics.log   per-GPU samples every 30 s
  node01..04/hw.txt        GPU index <-> PCI bus id, NIC <-> netdev
  truth.json               ground truth (diagnosers never read it)
```

Each scenario has one injected fault with a causal cascade, plus distractors in every scenario: a
benign Xid 13 from a previous job, a NIC flap that recovers within 2 s, NCCL plugin warnings,
correctable ECC counts, one hot but unthrottled GPU, and up to ±3 s of per-node clock skew (peers
often log the network failure *before* the faulty host's kernel does).

## Run it

Requirements: Go 1.23+. For the agent, an Ollama server with a tool-calling model
(`qwen3.5:9b` was used).

```
go run ./cmd/faultline gen -split dev  -out data
go run ./cmd/faultline gen -split test -out data

go run ./cmd/faultline rules        -data data/test
go run ./cmd/faultline rules -full  -data data/test
go run ./cmd/faultline agent -data data/test -model qwen3.5:9b -out results/agent-test.json -traces results/traces-test

go test ./...
```

Generation is deterministic: the same seed produces byte-identical files, and the test
`TestGenerationIsDeterministic` checks it. The agent tests drive the loop against a scripted fake
Ollama server, so they need no model.

On the machine used here, Windows Application Control intermittently blocked freshly built test and
`go run` binaries. The recorded runs therefore used a Linux build (`GOOS=linux go build`) executed
in WSL, next to the Ollama server.

## Layout

```
cmd/faultline        CLI: gen, rules, agent
internal/scenario    fault model, generator, splits
internal/logs        streaming parser (folds tracebacks into their record)
internal/rules       signature baselines
internal/agent       tools, Ollama client, loop, history compaction
internal/eval        diagnosis contract and frozen scoring
results/             recorded runs, per-scenario JSON, agent transcripts
```

## Limitations

See [RESULTS.md](RESULTS.md#limitations). In short: synthetic data written by the same author as the
diagnosers, one root cause per scenario, a single 9B model on a laptop GPU, and small splits.
