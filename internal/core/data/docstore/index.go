package docstore

// Unique declares a unique index over the one key key returns: no two
// documents may share it, and a write that would is refused with
// [UniqueKeyTaken]. An empty key is not indexed, so any number of documents
// may have none.
func Unique[T any](name string, key func(T) string) IndexSpec[T] {
	//: a nil function stays nil, for the engine's constructor to refuse by
	//: name.
	if key == nil {
		//: the declaration, without a way to compute its keys.
		return IndexSpec[T]{Name: name, Unique: true}
	}
	//: one key, or none when it is empty.
	return IndexSpec[T]{Name: name, Unique: true, Keys: func(v T) []string {
		//: the empty key is not a key.
		if k := key(v); k != "" {
			//: the one key.
			return []string{k}
		}
		//: nothing to file.
		return nil
	}}
}

// Index declares an index where a document may have several keys and a key
// several documents — the members a task is shared with. Find reads it.
func Index[T any](name string, keys func(T) []string) IndexSpec[T] {
	//: the declaration as given; the engine's constructor checks it.
	return IndexSpec[T]{Name: name, Keys: keys}
}
