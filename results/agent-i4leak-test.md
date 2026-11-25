### agent qwen3.5:9b on data/test

| split | n | full | type | node | gpu | grounded | invalid citations | errors |
|---|---|---|---|---|---|---|---|---|
| test | 22 | 11/22 | 13 | 16 | 18 | 15 | 0 | 1 |

| scenario | expected | predicted | node | gpu | grounded | full |
|---|---|---|---|---|---|---|
| test-gpu_ecc_uncorrectable-0 | gpu_ecc_uncorrectable | gpu_ecc_uncorrectable | yes | yes | yes | yes |
| test-gpu_ecc_uncorrectable-1 | gpu_ecc_uncorrectable | gpu_ecc_uncorrectable | yes | yes | yes | yes |
| test-gpu_ecc_uncorrectable-2 | gpu_ecc_uncorrectable | gpu_ecc_uncorrectable | yes | yes | no | no |
| test-gpu_fell_off_bus-0 | gpu_fell_off_bus | gpu_fell_off_bus | yes | yes | yes | yes |
| test-gpu_fell_off_bus-1 | gpu_fell_off_bus | gpu_fell_off_bus | yes | yes | yes | yes |
| test-gpu_fell_off_bus-2 | gpu_fell_off_bus | gpu_fell_off_bus | yes | yes | yes | yes |
| test-gpu_thermal_slowdown-0 | gpu_thermal_slowdown | ib_link_down | no | no | no | no |
| test-gpu_thermal_slowdown-1 | gpu_thermal_slowdown | gpu_ecc_uncorrectable | no | yes | no | no |
| test-gpu_thermal_slowdown-2 | gpu_thermal_slowdown | gpu_ecc_uncorrectable | no | no | no | no |
| test-host_oom-0 | host_oom | host_oom | yes | yes | yes | yes |
| test-host_oom-1 | host_oom | error: no diagnosis within the round limit | no | no | no | no |
| test-host_oom-2 | host_oom | host_oom | yes | yes | yes | yes |
| test-ib_link_down-0 | ib_link_down | ib_link_down | no | yes | no | no |
| test-ib_link_down-1 | ib_link_down | ib_link_down | yes | yes | yes | yes |
| test-ib_link_down-2 | ib_link_down | ib_link_down | yes | yes | yes | yes |
| test-nvlink_error-0 | other | gpu_fell_off_bus | yes | yes | yes | no |
| test-nvlink_error-1 | other | gpu_ecc_uncorrectable | yes | yes | yes | no |
| test-nvlink_error-2 | other | gpu_ecc_uncorrectable | yes | yes | yes | no |
| test-nvlink_error-3 | other | gpu_fell_off_bus | yes | yes | yes | no |
| test-storage_full-0 | storage_full | gpu_ecc_uncorrectable | no | no | no | no |
| test-storage_full-1 | storage_full | storage_full | yes | yes | yes | yes |
| test-storage_full-2 | storage_full | storage_full | yes | yes | yes | yes |

