//go:build !windows

package seglog

import "os"

// syncDir makes a directory's entries durable: a rename or an unlink is
// published only once its directory is synced. The directory is only read,
// so its Close loses nothing; a failure there is still reported when the
// sync succeeded.
func syncDir(dir string) (err error) {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := d.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return d.Sync()
}
