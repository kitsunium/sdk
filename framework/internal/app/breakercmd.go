package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"

	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// breakerUsageText is `breaker`'s usage; %[1]s is the product's name.
const breakerUsageText string = `usage:
  %[1]s breaker [list]
      every port whose calls a breaker guards — its ID, <service>/port/<name>,
      as the graph names it —, and whether an operator isolated it
  %[1]s breaker isolate PORT
      open PORT's breaker until it is reset: its calls fail at once, without
      reaching what the port is bound to
  %[1]s breaker reset PORT
      take the isolation off: the breaker closes, its counts cleared
The isolation is kept in the data directory: every start applies it, and a
running product within five seconds.
`

// The breaker command (issue #483): an operator opens a port's breaker
// until it is reset, and closes it — never through a route (D13). It writes
// a mark in the data directory, which the product applies at its start and,
// running, within breakerPoll (port_breaker_run.go); it needs no product
// stopped, and reads no store. An app that declares no breaker has no such
// command: Main reaches it through a breaker (portBreaker.command), so a
// product without one links none of it.

// command is Main's `breaker` command of app a.
func (*breakerGuard) command(a *App) func(ctx context.Context, args []string) int {
	return func(_ context.Context, args []string) int { return a.breakerCommand(args, os.Stdout, os.Stderr) }
}

// breakerCommand runs `breaker …`.
func (a *App) breakerCommand(args []string, stdout, stderr io.Writer) int {
	verb, port, ok := breakerArgs(args)
	if !ok {
		fmt.Fprintf(stderr, breakerUsageText, a.name)
		return 2
	}
	fsys, err := a.breakerData()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if verb == "list" {
		return a.listBreakers(fsys, stdout)
	}
	return a.markBreaker(fsys, verb, port, stdout, stderr)
}

// breakerArgs reads `breaker`'s arguments: list, or isolate or reset and a
// port; ok is false for any other.
func breakerArgs(args []string) (verb, port string, ok bool) {
	switch {
	case len(args) == 0 || len(args) == 1 && args[0] == "list":
		return "list", "", true
	case len(args) == 2 && (args[0] == "isolate" || args[0] == "reset") && args[1] != "":
		return args[0], args[1], true
	}
	return "", "", false
}

// breakerData is the data directory the marks are kept in, made when it is
// not there yet.
func (a *App) breakerData() (vfs.FullFS, error) {
	if err := a.resolve(); err != nil {
		return nil, err
	}
	if a.dataDir == "" {
		return nil, failure(CodeAppConfig, "BREAKER_NO_DATA",
			"the product keeps its data in memory: an isolation has nowhere to be kept; set "+appPrefix(a.name)+"_DATA_DIR", nil)
	}
	if err := os.MkdirAll(a.dataDir, 0o700); err != nil {
		return nil, failure(CodeAppConfig, "DATA_DIR_INVALID", "the data directory cannot be created", err, errs.String("dir", a.dataDir))
	}
	return vfs.NewOS(a.dataDir)
}

// listBreakers prints each port a breaker guards, and whether an operator
// isolated it.
func (a *App) listBreakers(fsys vfs.FullFS, stdout io.Writer) int {
	for _, b := range a.breakers() {
		state := "not isolated"
		if _, err := fs.Stat(fsys, b.mark); err == nil {
			state = "isolated"
		}
		fmt.Fprintf(stdout, "%s\t%s\n", b.port, state)
	}
	return 0
}

// markBreaker writes, or takes off, the mark that isolates port's breaker.
func (a *App) markBreaker(fsys vfs.FullFS, verb, port string, stdout, stderr io.Writer) int {
	var mark string
	for _, b := range a.breakers() {
		if b.port == port {
			mark = b.mark
		}
	}
	if mark == "" {
		fmt.Fprintf(stderr, "%q is no port a breaker guards: %s breaker lists them\n", port, a.name)
		return 1
	}
	var err error
	if verb == "isolate" {
		if err = fsys.MkdirAll(path.Dir(mark), 0o700); err == nil {
			err = fsys.WriteAtomic(mark, []byte(port+"\n"), 0o600)
		}
	} else if err = fsys.Remove(mark); errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		fmt.Fprintf(stderr, "the breaker of %s: %v\n", port, err)
		return 1
	}
	done := "isolated"
	if verb == "reset" {
		done = "reset"
	}
	fmt.Fprintf(stdout, "%s: %s; a running product applies it within five seconds\n", port, done)
	return 0
}
