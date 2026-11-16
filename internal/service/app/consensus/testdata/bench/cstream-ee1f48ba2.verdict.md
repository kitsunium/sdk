### bench A/B: cstream on AMD EPYC 9V45 96-Core Processor, 4 vCPUs

20 rounds read from /home/runner/work/_temp/bench-ab/raw; 4 CPUs (the suite needs 4). A rule holds when the bound of the 95 % interval of its per-round values meets its threshold.

| rule | issue | value | n | low | median | high | threshold | verdict | mode |
|---|---|---|---|---|---|---|---|---|---|
| 920-stream-8mib-tmpfs | #920 | p99-ms/commit BenchmarkTCPCommitStream/tmpfs/rate=8MiB/paced+stream-4 / BenchmarkTCPCommitStream/tmpfs/rate=8MiB/paced-4 | 20 | 1.00527 | 1.00801 | 1.01067 | <= 1.10 | pass | report |
| 920-stream-8mib-disk | #920 | p99-ms/commit BenchmarkTCPCommitStream/data-disk/rate=8MiB/paced+stream-4 / BenchmarkTCPCommitStream/data-disk/rate=8MiB/paced-4 | 20 | 0.140203 | 0.990724 | 1.01796 | <= 1.10 | pass | report |
