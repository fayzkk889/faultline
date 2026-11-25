# Agent iteration log

Every change to the agent's prompt, tools or budget was evaluated on the **dev** split only.
The test split was run once per frozen configuration and never used to choose a change.
Model: `qwen3.5:9b` (Q4, Ollama 0.40.17 in WSL, RTX 5070 Laptop 8 GB), temperature 0, seed 7, num_ctx 16384.

| iter | change | dev full | notes |
|---|---|---|---|
| 1 | initial prompt, 10-call budget (smoke test on one scenario, not a full run) | 0/1 | Anchored on the benign Xid 13 from a previous job; never checked the job start; spent 3 calls reading hw.txt. |
| 2 | anomaly digest states the job start and marks each template's offset (`BEFORE job start`); budget 14 | stopped after 1/18 | First scenario (ECC): correctly ignored the pre-job Xid 13 and found the Xid 48 + `ecc_dbe` evidence, then never answered. 212k prompt tokens over 16 calls; requests approached num_ctx, Ollama dropped the oldest messages (system prompt included), and the model degraded into empty replies and repeated searches. When told to answer, it wrote prose instead of calling `diagnose`. Run stopped (406 s for one scenario). |
| 3 | harness: compact history (newest 3 tool outputs verbatim, older ones cut to 500 chars); after the budget only `diagnose` is offered; prompt asks the model to write down ids it may cite; trace records the largest request | **14/18** | Largest request 10.2k tokens (was ~16k). 23-124 s per scenario. Misses: all 3 thermal + ib-0. Thermal-0 and ib-0 and thermal-2: picked the pre-job Xid 13 despite the `BEFORE job start` tag (also mislabelled Xid 13 as ECC / fell-off-bus). Thermal-1: saw `clocks_event={hw_slowdown,...}` for the right GPU in `metrics_summary` but had no citable id; searches for `clocks_event` return the 30 earliest (healthy) samples. Model repeatedly tried to read the end of train.log. |
| 4 | anomaly digest splits DURING vs BEFORE the job; `metrics_summary` gives the first record id for each distinct categorical value; `search` gains `last=true` | **14/18** | Mean 51 s, max 90 s per scenario; largest request 9.2k tokens. Pre-job Xid 13 no longer chosen when anything in-job exists, but the failures moved: thermal-1, thermal-2 and ib-0 picked the *in-job* NIC flap that recovered within 2 s (the model even noted it recovered); storage-0 picked the pre-job Xid 13 even though the `Errno 28` record was listed under DURING. In thermal-1 it named the throttled GPU, then chose the flap. Conclusion: the evidence is now reachable; the remaining errors are the 9B model's adjudication. Deliberately not added: Xid-number knowledge in the prompt (that is signature hand-feeding). From here runs use a Linux build inside WSL, because Windows Application Control intermittently blocks freshly built binaries. |
| 5 | same harness as 4, model thinking enabled (`-think`) | 11/18 | Worse and slower (mean 75 s vs 51 s). Thinking strengthened the anchoring on the recovered NIC flap: all 3 thermal, 2 of 3 OOM and 2 of 3 IB scenarios answered `ib_link_down` on the flap node. |

Iteration 4 was frozen and run once on the test split. An independent review then found that the
agent design was **not** dev-only: the `STOPPED` flag in `metrics_summary`, the prompt line "a
missing signal can be evidence too", and one unit test came from knowing test variant B (a lost GPU
silently disappears from metrics), which never happens on dev. That run is kept as
`results/agent-i4leak-test.*` and is not the headline.

| iter | change | dev full | notes |
|---|---|---|---|
| v2 | remove the leak (STOPPED flag, prompt line, test); fix `\bslowdown\b` (never matched `hw_slowdown`, so throttled samples were never warnings and never reached the digest); hard tool budget; max 5 citations; unknown nodes rejected; anomaly digest exempt from compaction; neutral wording for pre-job templates; GPU guidance covers link faults | see RESULTS.md | Fixes come from the review, not from looking at test results. Dev run once as a regression check, then the test split twice. |

**Provenance gaps (from the review):** iterations 1 and 2 left no result files; the 212k-token and
406 s figures for iteration 2 come from a transcript that was deleted when the run was stopped. The
code for iteration 3 was not committed on its own (it is part of `cc26561`). Result files do not
record the run configuration; it is: `qwen3.5:9b` Q4, Ollama 0.40.17 in WSL, temperature 0, seed 7,
`num_ctx` 16384, thinking off unless noted.
