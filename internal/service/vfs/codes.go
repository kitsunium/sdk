// Package vfs — range 0.3.55.* (ADR 0056 service/vfs block).
package vfs

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.55.0 - 0.3.55.255

// CodeRootUnavailable identifies a root directory that could not be opened, or
// that is not a directory at all. It is reported by the constructor, so the
// refusal arrives where the program is wired rather than at the first write.
const CodeRootUnavailable errs.Code = 0x00_03_37_01 // 0.3.55.1

// CodeDirectorySyncFailed identifies the ONE failure that arrives after a
// successful rename: the directory entry could not be flushed to the device.
//
// It is a separate code from PublishFailed because it means the opposite
// thing. PublishFailed says nothing changed; this says the new content IS
// visible and may not survive a power loss.
const CodeDirectorySyncFailed errs.Code = 0x00_03_37_02 // 0.3.55.2
