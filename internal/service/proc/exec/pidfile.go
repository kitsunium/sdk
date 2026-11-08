//go:build (linux && !mips && !mipsle && !mips64 && !mips64le) || darwin || dragonfly || freebsd || netbsd || openbsd

package exec

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxSymlinks bounds the symbolic links one walk follows, as the kernel's
// MAXSYMLINKS does (40 on Linux, 32 on the BSDs): past it the path loops.
const maxSymlinks int = 40

// maxPIDFileBytes bounds what is read of a PID file: one decimal PID and its
// blanks fit many times over; more is not a PID file.
const maxPIDFileBytes int = 64

// groupOtherWrite is the group and others' write bits of a mode.
const groupOtherWrite uint32 = 0o022

// entryStat is what the walk reads of an entry's stat: the identity a descent
// is proven against, the owner and the mode its trust is judged on. Each
// platform's directory holder (heldDir) fills it from its own syscall.Stat_t,
// whose field types differ from one kernel to the next.
type entryStat struct {
	// dev and ino name the inode.
	dev, ino uint64
	// uid is the owner.
	uid uint32
	// mode is st_mode: the type bits and the permissions.
	mode uint32
}

// pidWalk walks a PID file's path from the root one component at a time, each
// directory held open (heldDir) so a rename cannot move the walk elsewhere, and
// records whether anyone but root and this process's user could have made what
// it went through.
type pidWalk struct {
	// dirs are the directories descended into, the root first; ".." pops.
	dirs []heldDir
	// stats are dirs' stats, index for index.
	stats []entryStat
	// euid is this process's effective user.
	euid int
	// links counts the symbolic links followed.
	links int
	// trusted stays true while every step was made by root or euid.
	trusted bool
}

// readPIDFile reads the PID the file at path holds, safely (see
// AttachPIDFile), and reports whether the file is trustworthy.
func readPIDFile(path string) (pid int, trusted bool, err error) {
	f, trusted, err := openPIDFile(path)
	//: a path or a file that cannot be read safely.
	if err != nil {
		//: PID_FILE_REFUSED, its reason in the fields.
		return 0, false, err
	}
	defer func() { swallowErr(f.Close()) }()
	//: one byte past the bound tells a long file from a full one.
	buf, rErr := io.ReadAll(io.LimitReader(f, int64(maxPIDFileBytes)+1))
	//: an unreadable file.
	if rErr != nil {
		//: the read's fault.
		return 0, false, wrapPIDFileRefused(rErr, errs.String("path", path), errs.String("why", "unreadable"))
	}
	pid, err = parsePID(buf, path)
	//: the content and the trust together.
	return pid, trusted, err
}

// parsePID reads one decimal PID, blanks around it allowed, no sign: the
// content of a PID file.
func parsePID(buf []byte, path string) (int, error) {
	//: a file longer than a PID and its blanks.
	if len(buf) > maxPIDFileBytes {
		//: refused, never truncated into a number.
		return 0, wrapPIDFileRefused(nil, errs.String("path", path), errs.String("why", "longer than a PID"))
	}
	text := strings.TrimSpace(string(buf))
	n, pErr := strconv.ParseUint(text, 10, 31)
	//: not one decimal number a pid_t holds, or zero.
	if pErr != nil || n == 0 || n > math.MaxInt32 {
		//: the content, named.
		return 0, wrapPIDFileRefused(pErr, errs.String("path", path), errs.String("why", "not one PID"))
	}
	//: the pid.
	return int(n), nil
}

// openPIDFile walks path and opens the regular file at its end without
// following a symbolic link or blocking, reporting whether the walk and the
// file are trustworthy.
func openPIDFile(path string) (f *os.File, trusted bool, err error) {
	//: only an absolute, clean path walks from a known place.
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		//: refused before any open.
		return nil, false, refusePath(path, "not an absolute clean path to a file")
	}
	w, err := newPIDWalk("/")
	//: the root itself could not be opened.
	if err != nil {
		//: the cause, named.
		return nil, false, wrapPIDFileRefused(err, errs.String("path", path), errs.String("why", "root unreadable"))
	}
	defer w.close()
	//: from the root to the file.
	return w.walk(splitPath(path), path)
}

// walk takes the components todo, every one but the last a step, the last
// the file; path names the PID file in errors.
func (w *pidWalk) walk(todo []string, path string) (f *os.File, trusted bool, err error) {
	//: one component at a time; the last is the file.
	for len(todo) > 1 {
		name := todo[0]
		todo = todo[1:]
		target, isLink, sErr := w.step(name)
		//: a component that cannot be walked.
		if sErr != nil {
			//: PID_FILE_REFUSED naming the path.
			return nil, false, wrapPIDFileRefused(sErr, errs.String("path", path), errs.String("why", "walk refused at "+name))
		}
		//: a symbolic link continues the walk at its target.
		if isLink {
			todo = w.follow(target, todo)
		}
		//: too many links: the path loops.
		if w.links > maxSymlinks {
			//: the kernel's ELOOP.
			return nil, false, wrapPIDFileRefused(syscall.ELOOP, errs.String("path", path), errs.String("why", "too many symbolic links"))
		}
	}
	//: a link whose target is empty or the root alone leaves no file.
	if len(todo) == 0 {
		//: nothing to open.
		return nil, false, refusePath(path, "not a regular file")
	}
	//: the file itself.
	return w.open(todo[0], path)
}

// refusePath is PID_FILE_REFUSED for a path refused as written.
func refusePath(path, why string) error {
	//: no cause: the path itself.
	return wrapPIDFileRefused(nil, errs.String("path", path), errs.String("why", why))
}

// splitPath returns the components of an absolute or relative path, empty
// ones dropped.
func splitPath(path string) []string {
	parts := strings.Split(path, "/")
	out := parts[:0]
	//: drop the empty components a leading, doubled or trailing slash makes.
	for _, p := range parts {
		//: a real component.
		if p != "" {
			out = append(out, p)
		}
	}
	//: the components, in order.
	return out
}

// newPIDWalk opens root — "/" for a PID file, a test's own directory in the
// tests — and starts a walk there: an absolute link target restarts at it, and
// its trust is its own stat's, as the real root's is.
func newPIDWalk(root string) (*pidWalk, error) {
	dir, st, err := openWalkRoot(root)
	//: no root.
	if err != nil {
		//: the kernel's word.
		return nil, err
	}
	euid := os.Geteuid()
	//: the root is trustworthy when root or euid owns it and nobody else writes it.
	trusted := vouched(st.uid, euid) && st.mode&groupOtherWrite == 0
	//: the walk, at the root.
	return &pidWalk{dirs: []heldDir{dir}, stats: []entryStat{st}, euid: euid, trusted: trusted}, nil
}

// close releases every directory the walk holds.
func (w *pidWalk) close() {
	//: each held directory.
	releaseDirs(w.dirs)
	w.dirs = nil
}

// top is the directory the walk stands in, and its stat.
func (w *pidWalk) top() (heldDir, entryStat) {
	i := len(w.dirs) - 1
	//: the last directory descended into.
	return w.dirs[i], w.stats[i]
}

// step takes one component that is not the last: "." stays, ".." climbs, a
// directory is descended into, a symbolic link is read and returned. Every
// step updates the walk's trust.
func (w *pidWalk) step(name string) (target string, isLink bool, err error) {
	//: by the component.
	switch name {
	//: here.
	case ".":
		//: nothing moves.
		return "", false, nil
	//: the directory above, as the kernel resolves it from this one.
	case "..":
		w.up()
		//: no link.
		return "", false, nil
	}
	dir, dirSt := w.top()
	st, err := dir.lstat(name)
	//: no such entry, or no search permission.
	if err != nil {
		//: the cause.
		return "", false, err
	}
	w.trusted = w.trusted && entryTrusted(dirSt, st.uid, w.euid)
	//: by its type.
	switch st.mode & syscall.S_IFMT {
	//: a link: its target continues the walk.
	case syscall.S_IFLNK:
		w.links++
		target, err = dir.readlink(name)
		//: the target, read through the held directory.
		return target, err == nil, err
	//: a directory: descend.
	case syscall.S_IFDIR:
		//: held open, and proven the entry just examined.
		return "", false, w.descend(name, st)
	//: anything else cannot hold the file.
	default:
		//: the kernel's ENOTDIR.
		return "", false, syscall.ENOTDIR
	}
}

// descend opens the directory name — the entry whose stat is want — and makes
// it the walk's position. A directory renamed between the stat and the open
// is refused.
func (w *pidWalk) descend(name string, want entryStat) error {
	parent, _ := w.top()
	dir, st, err := parent.openDir(name)
	//: refused by the kernel: gone, or replaced by a link.
	if err != nil {
		//: the kernel's word.
		return err
	}
	//: what was opened, not what was examined, is what counts.
	if st.dev != want.dev || st.ino != want.ino {
		dir.close()
		//: the entry changed under the walk.
		return syscall.ESTALE
	}
	w.dirs = append(w.dirs, dir)
	w.stats = append(w.stats, st)
	//: one level down.
	return nil
}

// up climbs one directory; the root's parent is the root.
func (w *pidWalk) up() {
	//: at the root already.
	if len(w.dirs) == 1 {
		return
	}
	i := len(w.dirs) - 1
	w.dirs[i].close()
	w.dirs = w.dirs[:i]
	w.stats = w.stats[:i]
}

// follow continues the walk at a link's target: an absolute target restarts
// at the walk's root, a relative one where the link lies.
func (w *pidWalk) follow(target string, rest []string) []string {
	//: an absolute target: back to the root.
	if filepath.IsAbs(target) {
		//: every directory above the root is let go.
		for len(w.dirs) > 1 {
			w.up()
		}
	}
	//: the target's components, then what remained.
	return append(splitPath(target), rest...)
}

// open opens the walk's last component as the file. The entry is first held
// and examined without following a symbolic link (heldDir.entry), so a link
// at the end, a FIFO or a device is refused by its type before anything reads
// it; only a regular file is then read, through a read proven to reach the
// inode examined (heldEntry.read).
func (w *pidWalk) open(name, path string) (*os.File, bool, error) {
	parent, dir := w.top()
	e, st, why, err := parent.entry(name)
	//: no such file, no search permission on the way, or no stat.
	if err != nil {
		//: the cause stays matchable (fs.ErrNotExist).
		return nil, false, wrapPIDFileRefused(err, errs.String("path", path), errs.String("why", why))
	}
	defer e.close()
	//: a link, a directory, a FIFO, a socket, a device: never read.
	if typeWhy, ok := fileRefusal(st.mode); !ok {
		//: PID_FILE_REFUSED naming the type.
		return nil, false, wrapPIDFileRefused(nil, errs.String("path", path), errs.String("why", typeWhy))
	}
	trusted := w.trusted && entryTrusted(dir, st.uid, w.euid) && st.mode&groupOtherWrite == 0
	f, why, err := e.read(path)
	//: a file this process may not read, or one replaced meanwhile.
	if err != nil {
		//: the cause.
		return nil, false, wrapPIDFileRefused(err, errs.String("path", path), errs.String("why", why))
	}
	//: the open file and its verdict.
	return f, trusted, nil
}

// fileRefusal names why an entry of mode cannot be a PID file, and reports
// whether it can.
func fileRefusal(mode uint32) (why string, ok bool) {
	//: by the type.
	switch mode & syscall.S_IFMT {
	//: the one type read.
	case syscall.S_IFREG:
		//: acceptable.
		return "", true
	//: a link at the end is never followed.
	case syscall.S_IFLNK:
		//: the issue's first refusal.
		return "a symbolic link", false
	//: everything else.
	default:
		//: a directory, a FIFO, a socket, a device.
		return "not a regular file", false
	}
}

// entryTrusted reports whether an entry owned by uid, in the directory dir,
// could only have been made by root or euid: both belong to one of them, and
// nobody else may write the directory — or it is sticky, so another user may
// add entries but not replace this one.
func entryTrusted(dir entryStat, uid uint32, euid int) bool {
	//: owners first.
	if !vouched(dir.uid, euid) || !vouched(uid, euid) {
		//: another user owns the directory or the entry.
		return false
	}
	//: closed to others, or sticky.
	return dir.mode&groupOtherWrite == 0 || dir.mode&syscall.S_ISVTX != 0
}

// vouched reports whether uid is root or euid.
func vouched(uid uint32, euid int) bool {
	//: the two users trusted.
	return uid == 0 || int64(uid) == int64(euid)
}

// errorOr returns err, or alt when err is nil.
func errorOr(err, alt error) error {
	//: the first cause known.
	if err != nil {
		//: err itself.
		return err
	}
	//: the substitute.
	return alt
}
