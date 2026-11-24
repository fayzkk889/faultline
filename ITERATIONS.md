# Agent iteration log

Every change to the agent's prompt, tools or budget was evaluated on the **dev** split only.
The test split was run once per frozen configuration and never used to choose a change.
Model: `qwen3.5:9b` (Q4, Ollama 0.40.17 in WSL, RTX 5070 Laptop 8 GB), temperature 0, seed 7, num_ctx 16384.

| iter | change | dev full | notes |
|---|---|---|---|
| 1 | initial prompt, 10-call budget (smoke test on one scenario, not a full run) | 0/1 | Anchored on the benign Xid 13 from a previous job; never checked the job start; spent 3 calls reading hw.txt. |
| 2 | anomaly digest states the job start and marks each template's offset (`BEFORE job start`); budget 14 | pending | |
