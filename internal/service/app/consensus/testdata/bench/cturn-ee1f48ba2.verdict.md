### bench A/B: cturn on AMD EPYC 7763 64-Core Processor, 4 vCPUs

20 rounds read from /home/runner/work/_temp/bench-ab/raw; 4 CPUs (the suite needs 4). A rule holds when the bound of the 95 % interval of its per-round values meets its threshold.

| rule | issue | value | n | low | median | high | threshold | verdict | mode |
|---|---|---|---|---|---|---|---|---|---|
| 912-turn-p99-2followers | #912 | p99-us/turn BenchmarkLeaderLoopTurnAt1000PerSecond/followers=2-4 head/base | 20 | 0.932952 | 0.958652 | 0.996603 | <= 1.00 | pass | report |
| 912-turn-p99-4followers | #912 | p99-us/turn BenchmarkLeaderLoopTurnAt1000PerSecond/followers=4-4 head/base | 20 | 0.896538 | 0.904935 | 0.928084 | <= 1.00 | pass | report |
