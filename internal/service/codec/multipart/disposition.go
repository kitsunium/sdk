// Package multipart — reading a part's Content-Disposition, and the one rule
// its filename is reduced by on every operating system.
package multipart

import (
	"mime"
	stdmp "mime/multipart"
	"strings"
)

// formDataDisposition is the disposition type RFC 7578 §4.2 requires of every
// part; a part carrying any other has no form-data name.
const formDataDisposition string = "form-data"

// pathSeparators are the two octets a filename= parameter can separate path
// elements with. Both are read as separators on every OS, because the sender
// wrote the path with ITS separator, not with the receiver's.
const pathSeparators string = `/\`

// dispositionOf reads a part's form-data name and its filename, parsing the
// Content-Disposition header once.
//
// It replaces (*mime/multipart.Part).FileName, which passes the parameter
// through filepath.Base — and filepath.Base is PLATFORM-DEPENDENT: it splits
// on '\' on Windows and not on Unix. The same bytes therefore decoded to
// `te.txt` on Windows and to `q"uo\te.txt` on Linux, and to `a.txt` or to
// `C:\Users\ada\a.txt` depending on the host for a path an old browser sent in
// full. A codec whose Unmarshal answers differently per operating system is
// not one codec, so the parameter is read here and reduced by [lastElement],
// whose rule does not depend on the host.
//
// The name follows mime/multipart.Part.FormName exactly: empty unless the
// disposition is form-data. A header that does not parse yields neither, which
// the caller refuses as a part with no form-data name.
func dispositionOf(src *stdmp.Part) (name, fileName string) {
	disposition, params, err := mime.ParseMediaType(src.Header.Get("Content-Disposition"))
	//: an unparseable disposition carries no name and no filename — the same
	//: reading mime/multipart gives it.
	if err != nil {
		//: the caller refuses the nameless part.
		return "", ""
	}
	//: RFC 7578 §4.2 names a field only through a form-data disposition.
	if disposition == formDataDisposition {
		name = params["name"]
	}
	//: ParseMediaType has already folded an RFC 2231 filename* into filename.
	return name, lastElement(params["filename"])
}

// lastElement reduces a filename= parameter to its last path element, reading
// '/' and '\' as separators whatever the OS this runs on.
//
// RFC 7578 §4.2 has the receiver drop "directory path information that may be
// present", and a sender writes that information with its OWN separator: a
// browser on Windows that sends a full path sends backslashes, and a server on
// Linux has to strip them just the same. So both octets separate, everywhere.
// Trailing separators are dropped first, as filepath.Base drops them, so
// `dir/` names `dir`; a value made of nothing else has no element and yields
// "", the value a part with no usable filename already carries.
//
// It removes directory information and nothing else. A colon is not a
// separator — it is an ordinary filename octet on Unix — and "." or ".." are
// returned as they came: a caller who turns FileName into a path still has
// its own name to validate, exactly as before.
func lastElement(fileName string) string {
	//: a trailing separator ends a directory's name rather than naming an
	//: empty element after it.
	trimmed := strings.TrimRight(fileName, pathSeparators)
	//: everything after the last separator of either kind; -1 keeps it all.
	return trimmed[strings.LastIndexAny(trimmed, pathSeparators)+1:]
}
