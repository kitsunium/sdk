// Package service is the contract of a service unit's life cycle (ADR 0214):
// systemd.service(5)'s directives as typed values, the states a unit passes
// through and the snapshot a consumer reads, the ports the engine reaches the
// world through, and the codes of the range 0.3.105.*.
//
// The engine is internal/service/proc/service: one actor per unit over a pure
// reducer. Nothing here spawns, signals or waits. The ports exist so the
// engine runs on the SDK's spawner, a PID adopter, a control group and an
// sd_notify socket — and so a test hands it doubles of each:
//
//   - Spawner starts the unit's processes (proc/exec by default);
//   - Attacher adopts a process the engine did not start (MAINPID=, PIDFile=);
//   - CgroupKill is the unit's control group: who belongs, and a kill of all;
//   - NotifySource delivers the unit's sd_notify messages;
//   - BusWatcher follows who owns a Type=dbus unit's BusName= (proc/dbus);
//   - Clock, the kernel's clock.Timed, is what every wait runs on.
//
// A directive's Go zero is its systemd default (ADR 0031); systemd's literal
// 0 and infinity are the constants Zero and Infinity. ParseCommand reads one
// Exec*= command's prefixes, "@-:+!|".
//
// The range 0.3.105.* was allocated to the engine and is declared here, at its
// path in the core (ADR 0160).
package service
