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

The rules baseline is written from dev-split logs only and committed before variant B and the
held-out fault exist in the code. The commit history is the proof. The same person wrote the
generator and both diagnosers, so the test split measures robustness to phrasings the rules author
did not see, not an independent benchmark. A second baseline, `rules-full`, adds the public NVIDIA
Xid catalog and broader phrases; it bounds what a better-informed rules author would get.

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

- `overview` — files, line counts, time ranges, job start/end, warning/error counts per file.
- `anomalies` — warning-or-worse lines clustered by template (digits and hex masked), with counts,
  first/last ids and nodes. Generic: it contains no fault knowledge.
- `search` — case-insensitive regex over all or one file, capped results.
- `context` — lines around an evidence id.
- `diagnose` — the final answer. The harness rejects it if an evidence id does not exist and lets
  the model retry (at most twice).

Model temperature 0 and a fixed seed; runs are still not guaranteed bit-identical across hardware.

## Known limitations

- Synthetic data; same author for generator and diagnosers.
- One root cause per scenario; real incidents can have several.
- Small model (9B, 4-bit) on a laptop GPU; results say nothing about larger models.
