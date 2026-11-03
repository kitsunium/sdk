package cache

// keySpace names every key one store writes. Each shares the hash tag of the
// prefix, so the store lives on one slot of a cluster and one script reaches
// all of an entry's keys.
type keySpace struct {
	base string
}

// newKeySpace is the key space of prefix.
func newKeySpace(prefix string) keySpace {
	//: "{prefix}:" opens every key.
	return keySpace{base: "{" + prefix + "}:"}
}

// value is the key holding key's encoded value.
func (k keySpace) value(key string) string {
	//: v for value.
	return k.base + "v:" + key
}

// tags is the key holding the set of tags key's entry carries.
func (k keySpace) tags(key string) string {
	//: t for the entry's tags.
	return k.base + "t:" + key
}

// group is the key holding the set of keys carrying tag.
func (k keySpace) group(tag string) string {
	//: g for a tag's group.
	return k.base + "g:" + tag
}

// lease is the key of key's cross-replica fill lease.
func (k keySpace) lease(key string) string {
	//: l for lease.
	return k.base + "l:" + key
}
