# Agent iteration log

Every change to the agent's prompt, tools or budget was evaluated on the **dev** split only.
The test split was run once per frozen configuration and never used to choose a change.
Model: `qwen3.5:9b` (Q4, Ollama 0.40.17 in WSL, RTX 5070 Laptop 8 GB), temperature 0, seed 7, num_ctx 16384.

| iter | change | dev full | notes |
|---|---|---|---|
| 1 | initial prompt, 10-call budget (smoke test on one scenario, not a full run) | 0/1 | Anchored on the benign Xid 13 from a previous job; never checked the job start; spent 3 calls reading hw.txt. |
| 2 | anomaly digest states the job start and marks each template's offset (`BEFORE job start`); budget 14 | stopped after 1/18 | First scenario (ECC): correctly ignored the pre-job Xid 13 and found the Xid 48 + `ecc_dbe` evidence, then never answered. 212k prompt tokens over 16 calls; requests approached num_ctx, Ollama dropped the oldest messages (system prompt included), and the model degraded into empty replies and repeated searches. When told to answer, it wrote prose instead of calling `diagnose`. Run stopped (406 s for one scenario). |
| 3 | harness: compact history (newest 3 tool outputs verbatim, older ones cut to 500 chars); after the budget only `diagnose` is offered; prompt asks the model to write down ids it may cite; trace records the largest request | **14/18** | Largest request 10.2k tokens (was ~16k). 23-124 s per scenario. Misses: all 3 thermal + ib-0. Thermal-0 and ib-0 and thermal-2: picked the pre-job Xid 13 despite the `BEFORE job start` tag (also mislabelled Xid 13 as ECC / fell-off-bus). Thermal-1: saw `clocks_event={hw_slowdown,...}` for the right GPU in `metrics_summary` but had no citable id; searches for `clocks_event` return the 30 earliest (healthy) samples. Model repeatedly tried to read the end of train.log. |
| 4 | anomaly digest splits DURING vs BEFORE the job; `metrics_summary` gives the first record id for each distinct categorical value; `search` gains `last=true` | pending | Deliberately not added: Xid-number knowledge in the prompt (that is signature hand-feeding). |
