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
