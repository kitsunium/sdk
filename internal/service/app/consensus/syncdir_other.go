//go:build !windows

package consensus

import "os"

// syncDir makes a directory's entries durable: a rename or an unlink is
// published only once its directory is synced (D4 lifecycle steps 3 and 6).
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
