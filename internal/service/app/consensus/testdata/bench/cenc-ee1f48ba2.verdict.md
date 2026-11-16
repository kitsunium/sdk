### bench A/B: cenc on AMD EPYC 7763 64-Core Processor, 4 vCPUs

20 rounds read from /home/runner/work/_temp/bench-ab/raw; 4 CPUs (the suite needs 1). A rule holds when the bound of the 95 % interval of its per-round values meets its threshold.

| rule | issue | value | n | low | median | high | threshold | verdict | mode |
|---|---|---|---|---|---|---|---|---|---|
| 912-bytes-2followers | #912 | B/op BenchmarkLeaderTurnEncoding/shared/followers=2-4 / BenchmarkLeaderTurnEncoding/perpeer/followers=2-4 | 20 | 0.11387 | 0.11387 | 0.11387 | <= 0.50 | pass | report |
| 912-bytes-4followers | #912 | B/op BenchmarkLeaderTurnEncoding/shared/followers=4-4 / BenchmarkLeaderTurnEncoding/perpeer/followers=4-4 | 20 | 0.0575959 | 0.0575959 | 0.0575959 | <= 0.25 | pass | report |
