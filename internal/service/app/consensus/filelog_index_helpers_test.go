package consensus

import "unsafe"

// locate is where synced entry i lies; the caller checked that it is synced
// and retained. Only the tests read an entry's place directly, so it lives
// with them rather than as production code nothing calls.
func (l *fileLog) locate(i uint64) entryLoc {
	tab, pos := l.tableOf(i)
	return entryLoc{seg: l.tables[tab].seg, off: int64(l.tables[tab].offs[pos]) * int64(recAlign)}
}

// indexBytes is the bytes the index keeps resident, the entries in memory
// aside, which the unsynced batch bounds: the tables' offsets, the tables,
// and the term runs (#1111).
func (l *fileLog) indexBytes() int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	b := int64(cap(l.tables))*int64(unsafe.Sizeof(segIndex{})) + int64(cap(l.runs))*int64(unsafe.Sizeof(termRun{}))
	for i := range l.tables {
		b += int64(cap(l.tables[i].offs)) * indexEntryBytes
	}
	return b
}
