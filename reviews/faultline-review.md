# faultline: independent review

Reviewer stance: skeptical principal engineer. Repo at `C:\dev\engineering-proof\delivery\faultline`, HEAD `1dc238e`. I did not modify the repo or run `agent`.

**What I ran.** I cross-compiled all four test packages and the CLI for Linux and ran them in WSL. All tests pass and `go vet` is clean.

**What reproduced:**
- `gen` for dev and test reproduces `data/` byte for byte.
- The generator at the freeze commit `fd036a0` produces the same dev split as HEAD.
- `rules` and `rules -full` reproduce `results/rules-dev.json`, `rules-test.json` and `rules-full-test.json` byte for byte.
- The `fd036a0` rules binary scores 18/18 on dev and 0/22 on test.
- Every agent number in ITERATIONS.md for iterations 3 to 5 matches the traces: scores, misses, mean and max wall time, and largest request.

**Problems at a glance.** The core problems are not about arithmetic:
- The test split was not held out from agent design.
- The methodology guard is weaker than stated.
- The headline agent-vs-rules comparison is not yet backed by any artifact.
- The benchmark is much easier to localize than the prose suggests.

---

## Blockers

### B1. The agent's tools and prompt were designed with test-split knowledge, contradicting "every change was evaluated on dev only"
**Evidence:**
- `internal/agent/tools.go:264-268`: `metrics_summary` flags a GPU with `STOPPED ... before node's last sample`.
- `internal/agent/agent.go:66`: the system prompt says "A missing signal can be evidence too (for example a GPU that stops reporting metrics)."
- The only scenario where a GPU stops reporting is test variant B of `gpu_fell_off_bus` (`internal/scenario/scenario.go:451-453`, "the lost GPU silently disappears from the samples").
- In dev (variant A) the lost GPU keeps emitting `error="... GPU is lost"` rows (`scenario.go:469-471`), so STOPPED can never fire on dev. `grep STOPPED results/iter/traces-*` finds nothing.
- The unit test for this feature generates a **test-split** scenario and checks it against test truth: `internal/agent/agent_test.go:193-222`, `scenario.Suite("test")` at line 194.
- Both the prompt line and the feature are in the first agent commit `19a4039`, which came after the test split was committed (`850cd88`).
- The hint also hurt dev. In `results/iter/traces-dev-i3/dev-gpu_thermal_slowdown-1.json` the model spends about 5 of its 14 calls searching for "stopped" GPUs before giving up.

**Fix:** pick one.
- Remove the STOPPED flag and the missing-signal prompt line, and re-run dev.
- Or keep them and state plainly in ITERATIONS.md and the results that the agent's tool design used knowledge of the test-B fell-off-bus behaviour. Treat test `gpu_fell_off_bus` agent results as not held out.

Either way, move the test fixture to a synthetic set built by hand, not `Suite("test")`.

### B2. The "methodology guard" does not prove what it claims
**Evidence:**
- DESIGN.md says the rules baseline was "committed before variant B ... exist[s] in the code. The commit history is the proof", and that the test split measures "phrasings the rules author did not see."
- DESIGN.md is committed **in the freeze commit `fd036a0`** and already lists every variant-B phrasing class (`git show fd036a0:DESIGN.md`, lines 80-83): "older NVRM wording without an Xid number, cgroup OOM reported by the scheduler, throttle reasons as a hex bitmask, quota errors instead of ENOSPC, NIC module errors instead of an explicit link-down". So the rules author knew the B phrasings when the rules were frozen.
- Commit times are 23:31:53 (freeze, about 1,900 lines), 23:33:28 (test split, about 100 lines of new generator code), 23:34:03 (rules-full) and 23:39:00 (agent, 980 lines).
- `results/rules-test.json` has mtime 23:33:20, 8 s before the test-split commit. This is a scripted replay of pre-written code.
- Git order proves what each snapshot contained, not what the author knew. Author dates can also be set freely.

**Fix:**
- Reword: "the rules were not changed after the test split was committed; the same author designed both and knew the categories of variant-B changes."
- Drop "did not see" and "the commit history is the proof."
- For a real guard, have someone else write variant B, or generate held-out phrasings from a source the author did not control.

### B3. The headline agent result does not exist yet; README and ITERATIONS describe it in the past tense
**Evidence:**
- README.md says "Results: [RESULTS.md]" and "See [RESULTS.md](RESULTS.md#limitations)". RESULTS.md does not exist.
- ITERATIONS.md says "The test split was run with this configuration only."
- No tracked file holds an agent test-split result. `results/agent-test-run1.{log,md}` and `results/traces-test-run1/` are untracked. At review time the `.md` was 0 bytes and the `.log` was growing, so a run appears to be in progress. I did not inspect it.

**Fix:**
- Do not publish until the run finishes.
- Commit the run's `.json`, `.md`, `.log` and traces together with the config used.
- Add RESULTS.md, or remove the links to it.
- Record that exactly one test run was made: for example, commit the frozen config hash and then the result in a separate commit.

### B4. The 0/22 "frozen rules" floor is uninformative; a trivial generic heuristic localizes 19/22 test scenarios
**Evidence:**
- Every root-evidence line in variant B was changed, so the frozen signature rules can only answer `other` (`rules.go:190-196`). 0/22 is guaranteed by construction, and `rules_test.go` pins it.
- The base rules' fallback is "earliest watchdog timeout" (`rules.go:191`), the one strategy DESIGN.md itself calls unreliable under clock skew.
- I wrote a fault-agnostic heuristic of about 40 lines: take the earliest in-job record that has error priority (kern `err`, train `ERROR`, scheduler error words) and whose masked template occurs exactly once, then map the PCI id through hw.txt. (Scratch script not kept; the same idea is now `internal/rules/generic.go`.)
  - On the test split it gets node, GPU and a grounded citation all right on **19/22**: every scenario except the 3 thermal ones, and including all 4 held-out NVLink.
  - On dev it gets 12/18. It misses dev IB, because the dev root line is `kern.info` while the test one is `kern.err` (`scenario.go:270` vs `272`).
- So localization and grounding are nearly free on test. The only hard part is the fault label, and the test split is easier to localize than dev.

**Fix:**
- Add a frozen generic baseline: rarity plus error priority plus rank exclusion, with no fault vocabulary. Report it next to the agent.
- Report type accuracy separately from localization.
- Without this, "agent beats rules on test" mostly measures that the rules could not read new strings.

---

## Should-fix

### S1. `grounded` can be gamed: there is no limit on the number of citations
- `internal/eval/eval.go:67-76`: one root id among any number of cited ids is enough.
- `parseDiagnosis` (`agent.go:316-354`) accepts unbounded evidence lists.
- It already inflates the partial columns. In `agent-dev-i4.json`, `dev-ib_link_down-0` names the wrong node (the flap, `node02/kern.log:28`) but also cites the true root `node04/kern.log:30`, so it scores `grounded=yes`.

Fix: cap evidence at about 3 ids, or score citation precision, and report grounded only when node is correct.

### S2. The anomaly digest structurally steers toward the NIC-flap distractor and hides thermal evidence
- `inferLevel` (`internal/logs/logs.go:254-255`) promotes "Link down" to Warn because of the word `down`. "Link up" stays Info.
- `Anomalies()` (`tools.go:101-164`) therefore lists the flap's down line with no recovery next to it.
- In thermal scenarios the throttled samples never appear in the digest. The DURING section of `traces-dev-i4/dev-gpu_thermal_slowdown-1.json` has exactly 3 templates: the NCCL plugin warning, the flap's `Link down`, and the scheduler TIMEOUT. The flap is the only hardware candidate the tool offers.
- Contributing bug: `warnWords` contains `slowdown` and `throttl\w*`, apparently to catch throttling. `\bslowdown\b` does not match `clocks_event=hw_slowdown` because `_` is a word character (checked: no match). That vocabulary is dead.
- So ITERATIONS iteration 4's conclusion, "the evidence is now reachable; the remaining errors are the 9B model's adjudication," overstates the case. All flap mistakes (thermal in i4 and i5, plus OOM and IB in i5) line up with what the digest shows.

Fix: show each anomaly's next record in the same file, or pair state transitions generically. Then re-measure before blaming the model, and soften the ITERATIONS conclusion.

### S3. The "BEFORE the job started ... cannot have caused this job's failure" wording is a benchmark-shaped rule
- `tools.go:160`. It neutralizes the Xid 13 distractor by stating something that is false in real operations: a prior Xid on a GPU is a prime suspect for the next failure on that GPU.
- It is dev-derived, not test-derived, so this is not a leak. But it means the Xid-13 distractor no longer tests judgment.

Fix: make it a neutral label ("before job start"), or say it is a benchmark-specific assumption.

### S4. Iteration provenance is incomplete
- Iteration 1's code was never committed.
- Iteration 2's artifacts were committed as empty files in `19a4039` and later deleted. Its numbers have no artifact: "212k prompt tokens over 16 calls", "406 s", and "was ~16k" in the iteration 3 row.
- Iteration 3 ran on uncommitted code. `agent-dev-i3.json` has mtime 00:04. Commit `cc26561` (00:06) already contains the iteration 4 tool changes (`first@`, the DURING/BEFORE split, `last=true`), so no commit reproduces iteration 3.
- No result file records the run config (model, think, steps, ctx, seed, Ollama version). i4 and i5 are distinguishable only by the prose in ITERATIONS. `Message` has no `thinking` field, so traces drop the thinking content.
- "Ollama 0.40.17" and the GPU model cannot be verified from the repo.

Fix: write a `run.json` (config, git SHA, model digest, Ollama version) next to every results file. Mark the iteration 1 and 2 numbers as unrecorded.

### S5. Conclusions are drawn from single runs on n=18
- "Thinking ... Worse" (11/18 vs 14/18) is a single deterministic run per setting on 3 scenarios per fault.
- Iterations 3 and 4 both score 14/18 with different misses: thermal-0 goes from fail to pass, storage-0 from pass to fail. That is run-to-run sensitivity of about the same size as the claimed effects.

Fix: report per-fault counts and add a few seeds (or perturbed scenario seeds). Phrase differences of 3/18 as inconclusive.

### S6. Agent loop correctness
- **Soft budget.** `ToolCalls` is checked only once per round (`agent.go:202`). A single message with several tool calls overshoots it: i3 traces record 15, 16 and 15 calls against `MaxSteps=14`. Fix: stop executing calls once the budget is hit.
- **Forced turn not enforced.** On the forced turn only `diagnose` is offered, but any other tool the model names is still executed (`agent.go:247-248`).
- **Error and rejected answers are scored.**
  - On error or after 3 rejections, `runEach` (`cmd/faultline/rules.go:48-52`) still scores the returned diagnosis for the partial columns.
  - After a chat error the diagnosis is `eval.Diagnosis{}`, whose `GPU` is 0, so the `gpu` column is credited whenever the truth GPU is 0.
  - The rules fallback answers `-1`, which earns `gpu` credit on every non-GPU fault: that accounts for all 9 of the `gpu` hits in `rules-test.json`.
  - Fix: zero all columns on error, and credit `gpu` only when `node` is right.
- **Node is not validated.** `parseDiagnosis` rejects unknown ids but accepts any node string (for example "node3"). Fix: validate against `set` nodes as is done for ids.
- **Compaction loses the digest.** `compact` (`agent.go:175-189`) cuts the anomaly digest to 500 bytes after 3 newer tool outputs, which removes most of the DURING list. The byte slicing can also split UTF-8 (nit).
- **Token counts may be understated (unsure).** `MaxPrompt` comes from `prompt_eval_count`. With Ollama KV-cache reuse this can count only newly evaluated tokens, so "largest request 9.2k / 10.2k tokens" may understate request size. Verify, or tokenize locally.

### S7. Scoring ambiguity for held-out and non-GPU faults
- NVLink truth requires the exact GPU (`scenario.go:122`, not overridden). The agent prompt says to use -1 when the fault "is not a single GPU." An NVLink fatal error is arguably a link or NVSwitch fault, so -1 is a defensible answer that scores wrong.
- `host_oom` kills one rank's DataLoader, yet the GPU must be -1.

Fix: document the per-fault GPU convention in DESIGN.md "Scoring" and the system prompt, or accept either answer where the label is ambiguous.

### S8. Testing gaps
- `internal/logs` has no tests. Untested: `inferLevel`, kernel priority mapping, traceback folding, a train line with an unknown level (it is silently folded into the previous record and loses its citable id, `logs.go:150-156`), and CRLF input.
- There is no test that root evidence is at or after `fault_time`, sits on the truth GPU, or excludes distractors. `TestEveryLineParsesAndRootEvidenceResolves` only checks the node.
- There is no leak test that asserts tool outputs (`Anomalies`, `Overview`, `MetricsSummary`) contain no fault labels or truth ids across all scenarios.
- `TestGenerationIsDeterministic` checks one spec within one process. Pinning a hash of the full dev and test trees would catch generator drift that `TestRecordedBaselineNumbers` (counts only) misses.
- The compaction assertion in `TestLoopForcesAnswerAfterBudget` (`agent_test.go:142`) only checks one of compacted or short.
- Nothing tests budget overshoot, the error-path scoring, or `Score` with `Expected == "other"`.

### S9. Rules-full is labeled honestly, but "bounds what a better-informed rules author would get" overstates it
`rules.go:44-56` is written against the exact synthetic strings. For example, `oom_kill event .* on (node\d+)` keys on an "on nodeXX" suffix that the generator invents (`scenario.go:278`). It is an oracle for this generator, not a bound on a better-informed author. Call it "oracle signatures."

---

## Realism (where the synthetic data could mislead a reader)

1. **No in-job error-level noise.** Every distractor is either pre-job (Xid 13), info-level (flap) or metrics-only (SBE counts, hot GPU). Real job logs contain many benign in-job errors: retried I/O, NCCL WARNs, Python warnings, other Xids. This is why B4's heuristic works, and it contradicts README's "among thousands of symptom lines and benign noise."
2. **The flap is never on the faulty node** (`scenario.go:226`) and always falls 2-6 min after start. Both are exploitable regularities.
3. **Fell-off-bus B ("older driver without Xid") is inconsistent with the stated H100 hardware** (`hw.txt`: H100-SXM5). Every driver that supports H100 emits Xid 79. `RmInitAdapter failed` normally appears on (re)initialization, alongside or after Xid 79, not instead of it.
4. **ECC phrasing is inverted relative to hardware.** On Ampere and Hopper, uncorrectable ECC normally surfaces as Xid 94/95 with row remapping. Xid 48 DBE is the older-architecture form, yet dev (Xid 48 + 63) runs on H100s and B is described as "newer architecture." For contained ECC (Xid 94), I believe the application usually sees `uncorrectable ECC error encountered`, not "illegal memory access" (`scenario.go:255`). I am fairly but not fully sure.
5. **Cgroup OOM (B) would still appear in the host kernel log** as `Memory cgroup out of memory: Killed process ...`. The Slurm "Detected N oom_kill event" message comes from slurmstepd (job stderr or slurmd log), not the controller log, and has no "on nodeXX" suffix.
6. **IB cascade.** `status=12` (retry exceeded) completions usually make NCCL fail quickly with a remote or system error on the affected ranks, rather than every rank waiting for a 300 s watchdog (`scenario.go:406`). Two related points, both unsure: `localGid ::ffff:<IPv4>` is the RoCE form, not native InfiniBand (`scenario.go:342`), and `<netdev>: Link down` is the Ethernet-style mlx5 message.
7. **Storage scenario inconsistencies.**
   - The pre-failure log says `Saving checkpoint to .../step_001350`, which is off the 1000-step cadence (`scenario.go:392-394`).
   - The failure path is a different, random step: `step_009000` in `dev-storage_full-0` (`scenario.go:299`).
   - Watchdogs fire 300 s after `tf`, not after rank 0 actually dies 30-40 s later.
   - A periodic "Checkpoint saved" can be logged after the fault in other crash scenarios (`scenario.go:387-390`).
8. **Job outcome encodes the label.** Thermal is the only non-crash fault, so the scheduler `TIMEOUT` line identifies thermal perfectly, and `storage_full` is always `node01`. These are fine if acknowledged, but they make type and node partly free.
9. **Variant A `clocks_event` never shows idle**, while B uses `0x1` when idle. A uses `hw_slowdown,sw_thermal`, while B's mask also sets HW thermal (0x40). This is a minor cross-variant inconsistency.

---

## Nits
- DESIGN.md "Agent design" omits `metrics_summary`. Its fault table says the fell-off-bus rank "hangs," but nothing rank-specific is generated.
- README says "node01..04/train.log 32 ranks." Each file has 8 ranks.
- `jobStartRe` is duplicated with different regexes (`rules.go:40` vs `tools.go:166`).
- `chat` selects the diagnose tool with `tools[len(tools)-1:]`, which depends on the order of `toolSpecs` (`agent.go:129-131`).
- `Set.Unparsed` is computed but never surfaced to the tools or the CLI.
- `Truth.FaultTime` is written but never used.
- Stale `.bin/*.test.exe` and `rules2.test.exe` binaries are in the working tree (ignored by git, but confusing).

---

## Evidence check summary

| claim | status |
|---|---|
| Rules dev 18/18, test 0/22; rules-full 18/18, 22/22 | Reproduced byte for byte |
| Dev data unchanged since freeze | Reproduced (generator at `fd036a0` gives identical dev tree) |
| Iteration 3: 14/18, misses, 10.2k max request, 23-124 s | Matches traces (10208; 23.2-124.3 s) |
| Iteration 4: 14/18, mean 51 s, max 90 s, 9.2k | Matches (51.1 / 89.8 / 9247) |
| Iteration 5: 11/18, mean 75 s, flap pattern | Matches (74.7 s; all cited flap lines verified as `Link down` lines followed by `Link up`) |
| Iteration 3/4 Xid 13 picks (thermal-0, thermal-2, ib-0, storage-0) | Verified against the data lines |
| Iterations 1 and 2 numbers (212k tokens, 406 s, ~16k) | No artifact |
| Model, Ollama version, GPU, think on in i5 | Not recorded in any artifact |
| "Test split was run with this configuration only"; RESULTS.md | No committed artifact; run in progress; RESULTS.md missing |
| "Every change evaluated on dev only" | Contradicted (B1) |

## Overall verdict
The engineering is clean and honest in its mechanics: deterministic generation, reproducible baselines, verified citations, and an iteration log whose recorded numbers check out against the traces. A skeptical engineer would **not** accept the evaluation claims as currently written:
- The agent's tooling and prompt contain a feature that exists only for a test-split behaviour, and is tested on the test split (B1).
- The "frozen before the test split existed" guard is contradicted by DESIGN.md in the freeze commit (B2).
- The headline agent test result does not exist yet (B3).
- The 0/22 rules floor is an artifact of changing every signature, while a trivial fault-agnostic heuristic localizes 19/22 test scenarios (B4).

Fix B1-B4 first:
- Remove or disclose the leak.
- Reword the guard.
- Commit the single test run with its config.
- Add a generic baseline and report type accuracy separately from localization.

After that, the project would support a modest but defensible claim: on this synthetic bed, a 9B tool-using agent's main failure is fault-type adjudication among few, structurally simple distractors.
