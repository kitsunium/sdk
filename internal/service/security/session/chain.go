// Package session — the components ABOVE the store directory, which no flag on
// an open can reach.
//
// # What this closes
//
// FileConfig.Dir is a path, and every component of it is a name somebody else
// may have created. Measured against the store as it shipped, on darwin/arm64:
// a symbolic link planted at a parent component inside a world-writable sticky
// directory — the shape /tmp has — was followed by os.MkdirAll, the directory
// check then judged the TARGET, and the store was built and filed every record
// under the planter's tree:
//
//	Dir demandé    = …/pub/app/sessions     (pub is 1777, app -> …/elsewhere)
//	NewFileStore   : err=<nil>
//	New            : err=<nil>
//	record landed under the planted target = true
//
// The owner-only rule on Dir could not see it, because the directory it judged
// was a perfectly private 0700 one: the planter's, or one the store created
// inside the planter's tree.
//
// # The rule is the lock domain's, and so is the reason for it
//
// internal/service/app/lock closed the same gap for its lock directory (ADR 0083),
// on internal/kernel/fs/pathchain, and this is the same rule over the same walk:
// an indirection is refused when the directory HOLDING it is world-writable —
// when anybody could have planted it — and the sticky bit exempts nothing,
// because planting a component CREATES an entry rather than unlinking one.
//
// It is not "refuse a link". /tmp is a symbolic link on macOS, /var/run is one
// on most Linux distributions, and every t.TempDir() on macOS resolves through
// /var -> /private/var. What separates those from an attack is not the link,
// it is the directory the link lives in: one only root can write was set up by
// root.
//
// # What it does not see, and what does
//
// The audit runs once, at construction, BEFORE os.MkdirAll — auditing after it
// would mean refusing the directory only after creating it inside the
// planter's tree. A component replaced AFTER it returns is not seen by it; that
// half is closed by a different mechanism: the store holds its directory as an
// os.Root from construction on and resolves every name against that handle, so
// a parent swapped later moves nothing (see file_store.go).
package session

import (
	"github.com/kitsunium/sdk/internal/kernel/fs/pathchain"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	coresession "github.com/kitsunium/sdk/internal/core/security/session"
)

// kindIndirection names what was found at a component of Dir.
//
// It is deliberately not "symlink": a component is judged through
// fs.FileMode, which maps a Unix symbolic link, a Windows symbolic link and a
// Windows junction onto the same bit, so naming one of the three would be a
// guess. The target field says where it goes, which is what an operator needs.
const kindIndirection string = "indirection"

// checkChain refuses Dir when any existing component of its path is an
// indirection that anybody could have planted.
//
// pathchain.Resolve stops at the first component that does not exist yet and
// reports that as an answer rather than a fault — the store is usually about
// to create the last component or two — so a fresh directory is audited
// exactly as far as it exists.
func checkChain(dir string) error {
	steps, resolveErr := pathchain.Resolve(dir)
	//: the path could not be walked at all — a permission denied on an
	//: ancestor, a file where a directory belongs, an indirection loop.
	if resolveErr != nil {
		//: StoreUnavailable, the verdict os.MkdirAll would have reached.
		return wrapAs(coresession.StoreUnavailable, resolveErr, kerrs.String("op", "resolve-dir"))
	}
	//: one verdict per component that exists.
	for _, step := range steps {
		//: an ordinary directory redirects nothing.
		if !step.Indirect {
			//: next component.
			continue
		}
		plantedByAnyone, observed := plantable(step.Container)
		//: an indirection only a trusted account could have created is the
		//: operating system's own arrangement.
		if !plantedByAnyone {
			//: next component.
			continue
		}
		//: PathRedirected, naming the component rather than Dir.
		return chainRedirected(dir, step, observed)
	}
	//: every component is a real directory or an indirection nobody outside
	//: the trusted accounts could have put there.
	return nil
}

// chainRedirected builds the [coresession.PathRedirected] refusal for a component of Dir.
//
// It names both paths on purpose: Dir is what the operator configured and will
// search for, and the component is where the redirection actually is — usually
// neither the first nor the last thing they would have looked at. Paths are
// fields, never the Public string.
func chainRedirected(dir string, step pathchain.StepValue, observed string) error {
	//: the remedy is a human looking at the directory, never a retry.
	return wrapAs(coresession.PathRedirected, nil,
		kerrs.String("path", step.Path),
		kerrs.String("dir", dir),
		kerrs.String("kind", kindIndirection),
		kerrs.String("target", step.Target),
		kerrs.String("observed", observed))
}
