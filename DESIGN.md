# faultline: design record

## Problem

When a multi-node GPU training job dies, almost every rank reports the same thing: a collective
operation timed out. The useful signal is usually one line in one host's kernel log, a metrics
exporter that stopped reading one GPU, or a scheduler note, written minutes before the timeouts.
An engineer (or an agent) has to find that line among thousands of symptom lines and benign noise,
join it to hardware topology (PCI bus id to GPU index), and say what failed and where.

faultline is a bounded, reproducible test bed for that task. It asks one question:
**given the logs of a failed or degraded job, does a diagnoser name the right fault, on the right
node and GPU, and cite a log line that actually shows the root cause?**

It is not a reproduction of any company's product or data. All logs are synthetic, generated from
a seeded fault model; formats are modeled on public conventions (Linux kernel/NVRM Xid lines,
Python logging tracebacks, Slurm controller messages, logfmt GPU metrics) but are not copies of real
logs.

## System contract

Input: a scenario directory.

```
scenario/
  scheduler.log                 cluster controller events (job start/end, drains, OOM notes)
  nodeNN/kern.log               kernel ring buffer as syslog lines (NVRM, mlx5, oom-killer)
  nodeNN/train.log              Python-logging output of the ranks on that node, with tracebacks
  nodeNN/metrics.log            logfmt GPU samples every 30 s
  nodeNN/hw.txt                 GPU index <-> PCI bus id, NIC device <-> port
  truth.json                    ground truth; never read by diagnosers
```

Output of every diagnoser (rules or agent): a `Diagnosis`

```json
{"fault":"gpu_fell_off_bus","node":"node03","gpu":5,"evidence":["node03/kern.log:812"],"explanation":"..."}
```

`fault` is one of: `gpu_fell_off_bus`, `gpu_ecc_uncorrectable`, `ib_link_down`, `host_oom`,
`gpu_thermal_slowdown`, `storage_full`, or `other`. `gpu` is -1 when the fault is not a GPU fault.
Evidence ids are `<relative file>:<1-based line>`.

## Scoring (frozen before any diagnoser was run)

Per scenario:

- `type`: predicted fault equals truth. Held-out fault types (absent from the known catalog) are
  expected to be answered `other`.
- `node`: predicted node equals truth node.
- `gpu`: predicted gpu equals truth gpu (for non-GPU faults truth is -1 and must be -1).
- `grounded`: at least one cited evidence id is in the truth's root-evidence set. Citations that
  do not exist in the logs count as invalid and are reported separately.
- `full`: type and node and gpu and grounded.

The headline number is `full`. Partial columns are reported so that localization ability and
citation honesty are visible separately.

## Fault model

One root fault per scenario, injected at time `tf`, with a causal cascade:

| fault | root evidence | cascade |
|---|---|---|
| gpu_fell_off_bus | kernel NVRM line on that GPU's PCI id | that rank hangs; every rank hits the NCCL watchdog timeout |
| gpu_ecc_uncorrectable | kernel Xid for an uncorrectable ECC error | that rank raises a CUDA ECC error; others time out |
| ib_link_down | kernel NIC link event on one node | IB completion errors on that node *and on its peers*; then timeouts |
| host_oom | oom-killer kills a dataloader worker | that rank's DataLoader dies; others time out |
| gpu_thermal_slowdown | metrics show one GPU clock-throttled | every rank's step time regresses (straggler); no crash; job hits time limit |
| storage_full | rank 0 checkpoint write fails | rank 0 exits; others time out |

Noise in every scenario: a benign Xid 13 from a previous job before this job started, a transient
NIC link flap that recovers within seconds on a different node, NCCL plugin warnings at startup,
correctable ECC counts and one hot-but-not-throttled GPU, and per-node clock skew of up to 3 s
(so "earliest error wins" is unreliable when peers report the same failure).

## Splits

- `dev`: the six known faults, phrasing variant A.
- `test`: the six known faults with phrasing variant B (realistic driver/runtime differences:
  older NVRM wording without an Xid number, cgroup OOM reported by the scheduler, throttle reasons as
  a hex bitmask, quota errors instead of ENOSPC, NIC module errors instead of an explicit link-down),
  plus a held-out fault type (NVLink fatal error) that should be answered `other`.

## Methodology guard

What the commit history does and does not prove (corrected after the independent review in
`../../reviews/faultline-review.md`):

- **Proven**: the rules *code* was committed (`fd036a0`) before variant B and the held-out fault
  existed in the generator (`850cd88`), and the dev split regenerates byte-identical afterwards.
  `rules` was not edited after the test data existed (its outputs are unchanged and pinned by tests).
- **Not proven, and not true**: that the rules author had not seen the test phrasings. This file,
  committed in `fd036a0`, already listed them, and the same session wrote the generator, the rules
  and the agent within minutes. The `rules` vs test result therefore shows how *exact-string*
  signatures behave under phrasing drift, not what an unaware engineer would have written.
- `rules-full` adds signatures for the exact test strings and the public Xid catalog. It is an
  oracle tuned to this generator, useful only to show the test split is solvable by signatures.
- `rules-generic` has no fault vocabulary at all: earliest in-job error-level record whose masked
  template occurs once. It shows how much of the task is plain localization.

**Disclosed leak (fixed before the clean test run):** an earlier agent version had a `STOPPED`
flag in `metrics_summary`, a prompt line about missing signals, and a unit test that generated
test-split data. All three were written knowing that test variant B drops a lost GPU's samples, a
case that never occurs on dev. They were removed in `0fcc420`. One test run made with the leaky
version is kept and labelled `i4leak`; it is not the headline result.

## Alternatives considered

- **Real public log corpora (e.g. Loghub)**: real text, but no ground-truth root cause for
  multi-node GPU jobs and no cross-source causality. Rejected for the headline measurement; a
  realism gap is listed under limitations.
- **Embedding search / vector DB over logs**: unnecessary at this size; regex search plus template
  clustering is cheaper, deterministic, and inspectable.
- **Giving the model the whole log**: scenarios exceed a small model's useful context, and it hides
  whether the model can navigate. The agent gets tools instead.
- **Multi-agent debate**: not justified before a single agent's failure modes are measured.

## Agent design

A single tool-using loop against a local model (Ollama). Tools:

- `overview` — files, record counts, time ranges, warning/error counts per file, every scheduler event.
- `anomalies` — warning-or-worse records clustered by template (numbers, hex and PCI ids masked),
  split into DURING the job and BEFORE it started, with counts, first/last ids, offsets from job
  start and nodes. Severity comes from kernel syslog priority, Python log levels, or generic
  operational words; no fault signatures.
- `metrics_summary` — per GPU: sample count, last sample, numeric ranges, and for categorical fields
  the first record id showing each distinct value.
- `search` — case-insensitive regex over all or one file, earliest or latest 30 matches.
- `context` — records around an id in the same file.
- `diagnose` — the final answer, rejected if it cites a non-existent id, more than five ids, or an
  unknown node; at most two retries.

History compaction: the newest three tool outputs and the anomaly digest are sent verbatim, older
outputs are cut to 500 characters. Ollama silently drops the oldest messages (the system prompt
first) when a request exceeds `num_ctx`, so the harness bounds requests itself. The tool budget is
hard (14 calls); after it only `diagnose` is offered or executed.

Model temperature 0 and a fixed seed; runs are still not guaranteed bit-identical across hardware.

## Known limitations

- Synthetic data; same author for generator and diagnosers.
- One root cause per scenario; real incidents can have several.
- Small model (9B, 4-bit) on a laptop GPU; results say nothing about larger models.
- Localization is easier than it looks: `rules-generic` finds the right node, GPU and a grounded
  line on most scenarios without any fault vocabulary. The hard part measured here is naming the fault.
- Realism gaps found in review (see RESULTS.md): no in-job error-level noise; the NIC flap is never on
  the faulty node; the scheduler's TIMEOUT line alone identifies the thermal case; storage faults are
  always on node01; IB completion errors would normally make NCCL fail fast rather than wait for the
  watchdog; a cgroup OOM would usually still appear in the host kernel log.
