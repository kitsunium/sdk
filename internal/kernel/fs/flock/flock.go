package flock

import "os"

// TryLock takes an exclusive lock on the whole of file without waiting.
//
// It reports (true, nil) when the lock is now held through file, (false, nil)
// when another open description holds it, and (false, err) when the call
// itself failed: the kernel's errno, or errors.ErrUnsupported where [Native] is
// false. Contention is an answer and never an error, so a caller cannot mistake
// a busy lock for a broken one, nor the reverse.
func TryLock(file *os.File) (held bool, err error) {
	//: the platform's primitive, chosen by build tag.
	return tryLock(file)
}

// Unlock releases the lock [TryLock] took through file.
//
// Closing the descriptor would release it too. Unlocking explicitly first
// keeps the two events apart on purpose: the lock is given up while the
// descriptor is still valid, so a failure to release is reportable rather than
// hidden behind a close that "worked".
func Unlock(file *os.File) error {
	//: the platform's primitive, chosen by build tag.
	return unlock(file)
}
