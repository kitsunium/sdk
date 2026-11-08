# internal/service/proc/service

The engine of a service unit's life cycle — `systemd.service(5)` for one
service, as typed values (ADR 0214). Import its facade,
`github.com/kitsunium/sdk/pkg/v1/proc/service`.

```go
start, _ := service.ParseCommand([]string{"/usr/bin/daemon", "--foreground"})
unit, err := service.NewUnit(&service.Config{
	Name:      "daemon",
	Type:      service.TypeNotify,
	Notify:    notifySource,            // service.ListenNotify() on Linux
	ExecStart: []service.Command{start},
	Restart:   service.RestartOnFailure,
	Watchdog:  30 * time.Second,
})
err = unit.Start(ctx)  // once READY=1 arrived
err = unit.Fail(ctx)   // a failed health check: the watchdog's path
err = unit.Close(ctx)  // stopped, the actor ended
```

- One actor per unit over a pure reducer: every decision replays from its
  events.
- Restart= follows Table 1 of `systemd.service(5)`; RestartSec= grows on the
  kernel's backoff curve; the start limiter counts every start.
- Observers on a bounded queue the unit never waits on; `State` is the level.
- Every wait runs on the injected clock.
- `Type=forking` adopts the daemon its command leaves: from `PIDFile=`, read
  by the `Attacher` alone, or guessed among the members of a cgroup that
  lists them.
- `FileDescriptorStoreMax=` keeps what a service stores with `FDSTORE=1`
  and hands it to its next main process alone, at fd 3 on (Linux).
- `SpawnInstance` runs a unit of a template per accepted connection
  (`Accept=yes`), the connection at fd 3, ended with its run.
