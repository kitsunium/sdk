package jsonenc

// Object is an object being written: the slice so far, and whether a member
// has been written yet, which decides the comma. It is a value — every
// method returns the object to carry on with, and the one the caller still
// holds is stale — and the zero Object is not usable: start with
// [BeginObject].
type Object struct {
	// dst is the slice so far, ending after the last member or the brace.
	dst []byte
	// started is whether a member has been written, which decides the comma.
	started bool
}

// BeginObject appends the opening brace of an object to dst and returns the
// object that writes its members after it.
func BeginObject(dst []byte) Object {
	return Object{dst: append(dst, '{')}
}

// key writes the separator, the escaped key and the colon.
func (o Object) key(key string) Object {
	//: a member after another is separated from it.
	if o.started {
		o.dst = append(o.dst, ',')
	}
	o.dst = AppendString(o.dst, key)
	o.dst = append(o.dst, ':')
	o.started = true
	return o
}

// String writes the member key with the string value, both escaped as
// [AppendString] escapes.
func (o Object) String(key, value string) Object {
	o = o.key(key)
	o.dst = AppendString(o.dst, value)
	return o
}

// Int writes the member key with the number v.
func (o Object) Int(key string, v int64) Object {
	o = o.key(key)
	o.dst = AppendInt(o.dst, v)
	return o
}

// Uint writes the member key with the unsigned number v, as [AppendUint] writes it.
func (o Object) Uint(key string, v uint64) Object {
	o = o.key(key)
	o.dst = AppendUint(o.dst, v)
	return o
}

// Bool writes the member key with the literal true or false.
func (o Object) Bool(key string, v bool) Object {
	o = o.key(key)
	//: the literal.
	if v {
		o.dst = append(o.dst, "true"...)
		return o
	}
	o.dst = append(o.dst, "false"...)
	return o
}

// Null writes the member key with the literal null.
func (o Object) Null(key string) Object {
	o = o.key(key)
	o.dst = append(o.dst, "null"...)
	return o
}

// Raw writes the member key with raw, which the caller guarantees is one
// valid JSON value: it is copied as it is, so a nested object built by
// another Object, or a number the caller formatted, goes in without a
// second escape.
func (o Object) Raw(key string, raw []byte) Object {
	o = o.key(key)
	o.dst = append(o.dst, raw...)
	return o
}

// End appends the closing brace and returns the slice.
func (o Object) End() []byte {
	return append(o.dst, '}')
}
