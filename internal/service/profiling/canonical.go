// Package profiling — hosts CanonicalName: one spelling for a function,
// whoever named it.
package profiling

import (
	"regexp"
	"strings"
)

var (
	// generics matches the instantiation a runtime name carries: "[...]".
	generics = regexp.MustCompile(`\[[^\[\]]*\]`)
	// closure matches the suffixes the compiler gives a function literal, a
	// go or defer wrapper, a method value: they are their function's code.
	closure = regexp.MustCompile(`(\.func\d+|\.gowrap\d+|\.deferwrap\d+|-fm|\.\d+)+$`)
)

// CanonicalName spells a function the same way whoever named it, so a frame
// of a profile or a dump meets the function a static analysis found:
//
//   - the runtime's "pkg.(*T).M" and go/types' "(*pkg.T).M" are both
//     "pkg.(*T).M"; "(pkg.T).M" is "pkg.T.M";
//   - an instantiation is dropped: "pkg.F[...]" is "pkg.F";
//   - a function literal, a go or defer wrapper and a method value are their
//     enclosing function: "pkg.F.func1.2", "pkg.F.gowrap1" and "pkg.T.M-fm"
//     are "pkg.F", "pkg.F" and "pkg.T.M".
func CanonicalName(name string) string {
	name = generics.ReplaceAllString(name, "")
	//: go/types puts the receiver, package-qualified, in parentheses first.
	if strings.HasPrefix(name, "(") {
		name = receiverFirst(name)
	}
	//: the enclosing function.
	return closure.ReplaceAllString(name, "")
}

// receiverFirst rewrites go/types' "(*pkg.T).M" as the runtime's
// "pkg.(*T).M", and "(pkg.T).M" as "pkg.T.M".
func receiverFirst(name string) string {
	end := strings.Index(name, ").")
	//: not a receiver spelling after all.
	if end <= 0 {
		//: unchanged.
		return name
	}
	recv, method := name[1:end], name[end+2:]
	pointer := strings.HasPrefix(recv, "*")
	recv = strings.TrimPrefix(recv, "*")
	dot := strings.LastIndex(recv, ".")
	//: a receiver with no package cannot be moved.
	if dot <= 0 {
		//: unchanged.
		return name
	}
	pkg, typ := recv[:dot], recv[dot+1:]
	//: the runtime keeps the pointer in parentheses after the package.
	if pointer {
		//: pkg.(*T).M.
		return pkg + ".(*" + typ + ")." + method
	}
	//: pkg.T.M.
	return pkg + "." + typ + "." + method
}
