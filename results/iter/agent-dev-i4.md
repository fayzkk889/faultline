### agent qwen3.5:9b on data/dev

| split | n | full | type | node | gpu | grounded | invalid citations | errors |
|---|---|---|---|---|---|---|---|---|
| dev | 18 | 14/18 | 15 | 14 | 15 | 15 | 0 | 0 |

| scenario | expected | predicted | node | gpu | grounded | full |
|---|---|---|---|---|---|---|
| dev-gpu_ecc_uncorrectable-0 | gpu_ecc_uncorrectable | gpu_ecc_uncorrectable | yes | yes | yes | yes |
| dev-gpu_ecc_uncorrectable-1 | gpu_ecc_uncorrectable | gpu_ecc_uncorrectable | yes | yes | yes | yes |
| dev-gpu_ecc_uncorrectable-2 | gpu_ecc_uncorrectable | gpu_ecc_uncorrectable | yes | yes | yes | yes |
| dev-gpu_fell_off_bus-0 | gpu_fell_off_bus | gpu_fell_off_bus | yes | yes | yes | yes |
| dev-gpu_fell_off_bus-1 | gpu_fell_off_bus | gpu_fell_off_bus | yes | yes | yes | yes |
| dev-gpu_fell_off_bus-2 | gpu_fell_off_bus | gpu_fell_off_bus | yes | yes | yes | yes |
| dev-gpu_thermal_slowdown-0 | gpu_thermal_slowdown | gpu_thermal_slowdown | yes | yes | yes | yes |
| dev-gpu_thermal_slowdown-1 | gpu_thermal_slowdown | ib_link_down | no | no | no | no |
| dev-gpu_thermal_slowdown-2 | gpu_thermal_slowdown | ib_link_down | no | no | no | no |
| dev-host_oom-0 | host_oom | host_oom | yes | yes | yes | yes |
| dev-host_oom-1 | host_oom | host_oom | yes | yes | yes | yes |
| dev-host_oom-2 | host_oom | host_oom | yes | yes | yes | yes |
| dev-ib_link_down-0 | ib_link_down | ib_link_down | no | yes | yes | no |
| dev-ib_link_down-1 | ib_link_down | ib_link_down | yes | yes | yes | yes |
| dev-ib_link_down-2 | ib_link_down | ib_link_down | yes | yes | yes | yes |
| dev-storage_full-0 | storage_full | gpu_ecc_uncorrectable | no | no | no | no |
| dev-storage_full-1 | storage_full | storage_full | yes | yes | yes | yes |
| dev-storage_full-2 | storage_full | storage_full | yes | yes | yes | yes |

