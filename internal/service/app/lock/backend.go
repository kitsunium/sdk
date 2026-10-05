package lock

import "github.com/kitsunium/sdk/internal/kernel/fs/flock"

// platformNative reports that this GOOS has both halves the file locker rests
// on: the kernel's file lock and an open that refuses an indirection planted
// at the lock path. NewFileLocker reads it and refuses at CONSTRUCTION where it
// is false, so the refusal arrives where the program is wired rather than at
// the first contended section — and no locker is ever built that would report
// success while excluding nothing.
const platformNative bool = flock.Native && hardenedOpen
