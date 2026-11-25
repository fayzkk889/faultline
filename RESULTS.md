# Results

Every number below is read from a file in `results/`. Agent configuration for all `v2` runs:
`qwen3.5:9b` (Q4_K_M), Ollama 0.40.17 in WSL Ubuntu, RTX 5070 Laptop GPU (8 GB, ~19% of the model
offloaded to CPU), temperature 0, seed 7, `num_ctx` 16384, thinking off, 14-call budget, code at
commit `0fcc420`. Rules baselines are deterministic and pinned by `internal/rules/rules_test.go`.

## Headline

Test split: 22 scenarios, 18 known faults with phrasing variant B plus 4 held-out NVLink faults
whose correct label is `other`.

| diagnoser | full | fault type | node | gpu | grounded | citation precision |
|---|---|---|---|---|---|---|
| `rules` (frozen, exact strings from dev) | 0/22 | 4 | 3 | 9 | 0 | 0.00 |
| `rules-generic` (no fault vocabulary) | 4/22 | 4 | 19 | 19 | 19 | 0.86 |
| `rules-full` (oracle for these strings) | 22/22 | 22 | 22 | 22 | 22 | 1.00 |
| agent v2, run 1 | 13/22 | 13 | 17 | 19 | 17 | 0.75 |
| agent v2, run 2 | 14/22 | 15 | 17 | 18 | 18 | 0.77 |

Sources: `results/rules-test.json`, `rules-generic-test.json`, `rules-full-test.json`,
`agent-v2-test-run1.json`, `agent-v2-test-run2.json`.

Reading it correctly:

- `rules-generic` gets its 4 only on the held-out NVLink cases, because it always answers `other`.
  Its real contribution is the node column: **localization is mostly mechanical here** (19/22 with
  no fault knowledge). The hard, measured part is naming the fault.
- `gpu` is inflated for every diagnoser that answers -1 on non-GPU faults (OOM, storage, IB truth is
  -1), so the frozen rules get 9 GPU "hits" while being wrong everywhere. Use `full`.
- `rules-full` is not a fair baseline; it shows only that the split is solvable by signatures.

## Per fault, test split (full / n)

| fault | rules | rules-generic | rules-full | agent run 1 | agent run 2 |
|---|---|---|---|---|---|
| ECC uncorrectable (Xid 94) | 0/3 | 0/3 | 3/3 | 3/3 | 2/3 |
| GPU fell off bus (no Xid, re-init fails) | 0/3 | 0/3 | 3/3 | 3/3 | 3/3 |
| IB transceiver error (no link-down line) | 0/3 | 0/3 | 3/3 | 3/3 | 2/3 |
| host OOM (reported by the scheduler) | 0/3 | 0/3 | 3/3 | 2/3 | 3/3 |
| storage quota (Errno 122) | 0/3 | 0/3 | 3/3 | 2/3 | 3/3 |
| thermal (NVML hex bitmask) | 0/3 | 0/3 | 3/3 | 0/3 | 0/3 |
| NVLink, held out (expects `other`) | 0/4 | 4/4 | 4/4 | 0/4 | 1/4 |

On the 18 known-fault scenarios the agent is 13/18 in both runs; frozen exact-string rules are 0/18.

## Dev split (agent tuned here)

| diagnoser | full | type | node | grounded | precision |
|---|---|---|---|---|---|
| `rules` | 18/18 | 18 | 18 | 18 | 1.00 |
| `rules-generic` | 0/18 | 0 | 12 | 12 | 0.67 |
| agent v2 (one run, regression check) | 13/18 | 16 | 13 | 13 | 0.72 |

Agent dev misses: all three IB scenarios named the node of the recovered NIC flap instead of the
link that never came back; one thermal and one storage case also chose the flap.

## What the agent gets wrong, from the transcripts

1. **It never says "I don't know".** On held-out NVLink faults it usually localizes the right node
   and GPU, then forces a known label (`gpu_fell_off_bus` or `gpu_ecc_uncorrectable`). One `other`
   answer in eight attempts.
2. **Anchoring on a recovered transient.** The NIC flap that recovers in about 2 s is chosen as the
   root cause across fault types. The anomaly digest shows both `Link down` lines as warnings and
   never shows the `Link up` that marks one as recovered (the review predicted this). It is a tool
   limitation as much as a model one, and it was **not** patched after seeing these results.
3. **Cannot read an NVML throttle bitmask.** Thermal variant B reports `clocks_event_reasons=0x...68`;
   the agent never decodes it (0/6 across runs).
4. Thinking mode made the 9B model worse on dev (11/18 vs 14/18 for iteration 4; ITERATIONS.md).

## Reproducibility and variance

- Generation and all rules baselines are deterministic and reproduce byte for byte (checked by the
  independent review and by tests).
- The agent is **not** deterministic despite temperature 0 and a fixed seed: the two test runs agree
  on the `full` outcome for 17/22 scenarios and give identical (fault, node) answers on 16/22.
  Treat single-run differences of one or two scenarios as noise.
- Cost per scenario (v2): mean 70-75 s wall time, 10.5-11.9 tool calls; the budget forced an
  answer in 6/18 dev and 10-13/22 test scenarios; no answer was rejected for invalid citations.
- Largest single request: 10.7k tokens on dev, 14.4k on test run 1, against a 16k window. Keeping
  the anomaly digest uncompacted brought requests close to the limit; a larger digest could again
  trigger Ollama's silent truncation.

## Earlier, contaminated run

`results/agent-i4leak-test.*`: iteration 4 configuration, which contained a test-split leak (see
DESIGN.md). 11/22 full, one run ended without a diagnosis (`test-host_oom-1`, round limit). Its JSON
predates the citation-precision field. Kept for transparency; not comparable as a clean result.

## Limitations

- Synthetic data, written by the same author (with AI assistance) as every diagnoser. The DESIGN.md
  in the freeze commit already described the test phrasings.
- Small splits (18 dev, 22 test); no confidence intervals; two agent runs on test, one on dev.
- One model, 4-bit, on a laptop GPU. Nothing here says how larger models behave.
- Realism gaps listed in DESIGN.md and the review: no in-job error-level noise, the flap is never on
  the faulty node, TIMEOUT alone identifies the thermal case, storage faults are always on node01,
  NCCL would usually fail fast on IB completion errors, a cgroup OOM would usually also reach the
  host kernel log, and the "older driver without Xid 79" variant does not match H100-era drivers.
- Fault-type accuracy is the meaningful axis; this test bed under-tests localization.
