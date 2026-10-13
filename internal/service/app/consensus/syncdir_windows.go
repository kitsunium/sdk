package consensus

// syncDir is a no-op on Windows: a directory cannot be opened for a flush
// there, and NTFS journals the metadata a rename or an unlink changes. The
// weaker guarantee is stated, as D4 states each platform's.
func syncDir(string) error { return nil }
