// Package ipc — range 0.3.90.* (ADR 0144).
package ipc

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.90.0 - 0.3.90.255

// CodeMisconfigured identifies a configuration no endpoint can be built from:
// an empty or relative path, one longer than the kernel's sun_path, a UID or
// GID list holding a negative value.
const CodeMisconfigured errs.Code = 0x00_03_5A_01 // 0.3.90.1

// CodeDirectoryUnsafe identifies a socket directory another account could
// plant an entry in or read through: not a directory, a link, not owned by
// this process's user, or writable by its group or by anyone.
const CodeDirectoryUnsafe errs.Code = 0x00_03_5A_02 // 0.3.90.2

// CodeInUse identifies a socket path a live process already answers on.
const CodeInUse errs.Code = 0x00_03_5A_03 // 0.3.90.3

// CodeListenFailed identifies a listen the kernel refused.
const CodeListenFailed errs.Code = 0x00_03_5A_04 // 0.3.90.4

// CodePeerRefused identifies a peer whose kernel-reported identity is not
// this process's user nor in the allow-lists.
const CodePeerRefused errs.Code = 0x00_03_5A_05 // 0.3.90.5

// CodeDialFailed identifies a dial that did not reach a listener.
const CodeDialFailed errs.Code = 0x00_03_5A_06 // 0.3.90.6

// CodeEndpointForeign identifies a socket file owned by another account: a
// client refuses to talk to what may be an impostor.
const CodeEndpointForeign errs.Code = 0x00_03_5A_07 // 0.3.90.7

// CodeClosed identifies an Accept on a listener that was closed.
const CodeClosed errs.Code = 0x00_03_5A_08 // 0.3.90.8
