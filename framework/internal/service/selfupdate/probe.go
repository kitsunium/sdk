package selfupdate

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// prevSuffix names the previous binary kept beside the new one.
const prevSuffix string = ".prev"

// defaultProbeTimeout bounds a probe that declared none.
const defaultProbeTimeout time.Duration = 5 * time.Second

// probeSpec is what a replacement must answer before it stands.
type probeSpec struct {
	args    []string
	timeout time.Duration
	run     func(ctx context.Context, path string, args []string) error
}

// linker is the FileSystem sibling that keeps the previous binary under a
// second name while the first still points at it.
type linker interface {
	Link(oldpath, newpath string) error
}

// withProbe is Service.WithProbe's body: decl_gen.go writes Service.WithProbe, from the
// design, as one call of it.
func (u *Service) withProbe(args []string, timeout time.Duration) *Service {
	if u == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	u.probe = probeSpec{args: slices.Clone(args), timeout: timeout, run: runProbe}
	return u
}

// keepPrevious links the running binary as <binary>.prev before it is
// replaced; false when the file system cannot link, and nothing is kept.
func (u *Service) keepPrevious(execPath string) bool {
	if len(u.probe.args) == 0 {
		return false
	}
	l, ok := u.fs.(linker)
	if !ok {
		return false
	}
	prev := execPath + prevSuffix
	//: a previous <binary>.prev is replaced; one that cannot be removed
	//: means nothing is kept rather than a stale binary kept.
	if err := u.fs.Remove(prev); err != nil && !errors.Is(err, os.ErrNotExist) {
		//: keep nothing: a rollback to an older binary is worse than none.
		return false
	}
	return l.Link(execPath, prev) == nil
}

// probeReplacement runs the declared probe against the binary now at
// execPath, and puts the kept previous binary back when it fails.
func (u *Service) probeReplacement(execPath string, kept bool) error {
	if len(u.probe.args) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), u.probe.timeout)
	defer cancel()
	err := u.probe.run(ctx, execPath, u.probe.args)
	if err == nil {
		return nil
	}
	rolledBack := false
	if kept {
		rolledBack = u.fs.Rename(execPath+prevSuffix, execPath) == nil
	}
	return classify(ProbeFailed, err, errs.String("path", execPath), errs.Bool("rolled_back", rolledBack),
		errs.String("timeout", u.probe.timeout.String()))
}

// runProbe runs the binary at path with args, output discarded, within ctx.
func runProbe(ctx context.Context, path string, args []string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = io.Discard, io.Discard, nil
	cmd.Env = append(os.Environ(), "SELFUPDATE_PROBE=1")
	err := cmd.Run()
	if ctx.Err() != nil {
		return errors.Join(err, ctx.Err())
	}
	return err
}
