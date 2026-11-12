package seglog

// syncDir is a no-op on Windows: a directory cannot be opened for a flush
// there, and NTFS journals the metadata a rename or an unlink changes. The
// weaker guarantee is stated, as the consensus log states it (ADR 0152 D4).
func syncDir(string) error { return nil }
