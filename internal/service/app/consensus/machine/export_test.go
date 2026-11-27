package machine

import "time"

// SetReadBackTimeout bounds m's read back of a failed restore by d, in
// place of readBackTimeout: a test reaches the bound without waiting it.
func SetReadBackTimeout(m *SQL, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readBackWait = d
}
