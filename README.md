# faultline

[![ci](https://github.com/fayzkk889/faultline/actions/workflows/ci.yml/badge.svg)](https://github.com/fayzkk889/faultline/actions/workflows/ci.yml)

**Can an agent find the root cause of a failed multi-node GPU training job from its logs, and prove it?**

When a distributed job dies, almost every rank reports the same collective timeout. The cause is
usually one line, minutes earlier, on one host: an Xid in a kernel log, a GPU that vanished from the
metrics exporter, a scheduler note about a cgroup OOM. It sits among benign noise, with clocks that
disagree across nodes by a few seconds.

faultline generates that situation with exact ground truth and scores diagnosers on it. An answer
counts only if it names the **fault**, the **node** and the **GPU**, *and* cites a log record that is
in the ground-truth evidence. Citations are checked mechanically, so a diagnoser cannot be right by
guessing.

## Findings

Test split: 22 scenarios. 18 are known faults whose log phrasing differs from the dev split (driver,
exporter and runtime differences); 4 are a held-out fault type whose correct answer is `other`.

| diagnoser | correct (full) | right node |
|---|---|---|
| signature rules, written from dev logs, frozen | 0/22 | 3 |
| no-vocabulary localizer (earliest unique in-job error) | 4/22 | 19 |
| signature rules written for the test strings (oracle) | 22/22 | 22 |
| tool-using agent, local 9B model, run 1 / run 2 | 13/22, 14/22 | 17, 17 |

1. **Exact-string signatures do not survive phrasing drift.** The frozen rules score 0/18 on known
   faults; the agent scores 13/18 in both runs.
2. **Localization is mostly mechanical; naming the fault is the hard part.** A localizer with no fault
   vocabulary finds the right node on 19/22.
3. **The agent never abstains.** On the held-out NVLink fault it usually finds the right GPU, then
   forces a known label: `other` once in eight attempts.
4. **Its dominant error is anchoring on a transient that recovered** (a NIC flap that comes back in 2 s),
   and it cannot decode an NVML throttle bitmask (0/6).
5. **Temperature 0 is not determinism.** Two identical runs agree on 17 of 22 outcomes.
6. **Most early agent failures were harness bugs, not model failures.** Ollama silently drops the oldest
   messages (the system prompt first) when a request exceeds `num_ctx`; the fix was bounded history,
   not prompting. Thinking mode made this model worse (11/18 vs 14/18 on dev).

Full tables, per-fault results, costs and variance: [RESULTS.md](RESULTS.md).

## How the measurement is kept honest

- **Frozen baseline.** The signature rules were committed (`fd036a0`) before the test phrasings existed
  in the generator (`850cd88`); the dev split regenerates byte-identical afterwards. DESIGN.md states
  exactly what this does and does not prove.
- **Dev-only tuning.** Every agent change and its dev score is in [ITERATIONS.md](ITERATIONS.md),
  including the ones that made things worse.
- **Independent review.** A separate reviewer with no stake in the result
  ([reviews/faultline-review.md](reviews/faultline-review.md)) found a test-split leak in an earlier
  agent version, an overstated methodology claim, a regex bug, and a weak floor. All were fixed or
  disclosed before the clean runs; the contaminated run is kept and labelled.
- **CI reproduces the published numbers.** Every baseline result file in `results/` is regenerated from
  scratch on each push and must match byte for byte. Agent transcripts for every run are committed.

## What a scenario contains

```
data/test/test-gpu_fell_off_bus-0/
  scheduler.log            job start/end, cgroup OOM notes
  node01..04/kern.log      kernel lines with syslog priority (NVRM/Xid, mlx5, oom-killer, noise)
  node01..04/train.log     32 ranks: steps, NCCL warnings, tracebacks, watchdog timeouts
  node01..04/metrics.log   per-GPU logfmt samples every 30 s
  node01..04/hw.txt        GPU index <-> PCI bus id, NIC <-> netdev
  truth.json               ground truth (never read by diagnosers)
```

Six fault types with causal cascades (GPU off the bus, uncorrectable ECC, InfiniBand link, host OOM,
thermal slowdown, storage full) plus a held-out NVLink fault. Distractors in every scenario: a benign
Xid 13 from a previous job, a NIC flap that recovers, NCCL plugin warnings, correctable ECC counts, a
hot but unthrottled GPU, and up to ±3 s of per-node clock skew (peers often log a network failure
before the faulty host's kernel does). All data is synthetic; formats follow public conventions and
are not copied from any real system.

## The agent

A single tool-using loop against a local model through Ollama, with read-only, fault-agnostic tools:
an overview, a template-clustered anomaly digest split into during and before the job, a per-GPU
metrics summary that cites where each categorical value first appeared, regex search, and context.
The final `diagnose` call is rejected if it cites a record that does not exist, cites more than five,
or names an unknown node. History is compacted so requests stay inside the context window, and the
tool budget is hard. Design rationale and rejected alternatives: [DESIGN.md](DESIGN.md).

## Run it

Go 1.23+. The agent additionally needs an Ollama server with a tool-calling model.

```sh
go run ./cmd/faultline gen -split dev  -out data
go run ./cmd/faultline gen -split test -out data

go run ./cmd/faultline rules          -data data/test   # frozen signatures
go run ./cmd/faultline rules -generic -data data/test   # localization only
go run ./cmd/faultline rules -full    -data data/test   # oracle

go run ./cmd/faultline agent -data data/test -model qwen3.5:9b \
  -out results/agent-test.json -traces results/traces-test

go test ./...
```

The tests need no model: the agent loop is exercised against a scripted fake Ollama server.

```
cmd/faultline        CLI: gen, rules, agent
internal/scenario    fault model, generator, splits
internal/logs        streaming parser; tracebacks fold into their record
internal/rules       signature baselines and the no-vocabulary localizer
internal/agent       tools, Ollama client, loop, history compaction
internal/eval        diagnosis contract and frozen scoring
results/             recorded runs, per-scenario JSON, agent transcripts
```

## Limitations

Synthetic data written by the same author as every diagnoser; small splits (18 dev, 22 test) with two
agent runs on test; one 4-bit 9B model on a laptop GPU; one root cause per scenario; known realism
gaps listed in [RESULTS.md](RESULTS.md#limitations). The claim is narrow: on this bed, a small local
agent mostly fails at naming the fault and at saying "unknown", not at finding where to look.

## Authorship

Built by Faizan Khan with substantial AI assistance (Claude Code) for implementation, runs and
review. MIT licensed.
