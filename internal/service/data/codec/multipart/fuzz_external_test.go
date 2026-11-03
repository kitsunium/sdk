package multipart_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/multipart"
)

// FuzzUnmarshal is #897's multipart target, over a body framed by a boundary
// the input picks: any body decodes without a panic; a body that decodes was
// framed by the boundary Boundary sniffs from it — sniff and confirm agree —
// and its parts re-encode under that boundary, decode back equal, and
// re-encode to the same bytes.
func FuzzUnmarshal(f *testing.F) {
	c := multipart.New()
	sample, err := c.Marshal(multipart.FormValue{Boundary: "b0und", Parts: []multipart.PartValue{
		{Name: "field", Data: []byte("value")},
		{Name: "file", FileName: "dir/cv.pdf", ContentType: "application/pdf", Data: []byte("%PDF\r\n--b0und")},
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Add("b0und", sample)
	f.Add("x", []byte("--x\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\n1\r\n--x--\r\n"))
	f.Add("x", []byte("preamble\r\n--x--\r\n"))
	f.Add("", []byte("--\r\n"))
	f.Fuzz(func(t *testing.T, boundary string, tail []byte) {
		body := append([]byte("--"+boundary), tail...)
		var form multipart.FormValue
		//: decoded, or refused — never a panic.
		if err := c.Unmarshal(body, &form); err != nil {
			return
		}
		sniffed, err := multipart.Boundary(body)
		//: the boundary the decode used is the one sniffing finds.
		if err != nil || sniffed != form.Boundary {
			t.Fatalf("decoded under %q, Boundary sniffs %q (%v)", form.Boundary, sniffed, err)
		}
		out, err := c.Marshal(form)
		if err != nil {
			t.Fatalf("Marshal of a decoded form %+v: %v", form, err)
		}
		var again multipart.FormValue
		//: the parts read back as written.
		if err := c.Unmarshal(out, &again); err != nil || len(again.Parts) != len(form.Parts) {
			t.Fatalf("round trip of %q: %+v (%v), want %+v", out, again, err, form)
		}
		for i := range form.Parts {
			if !partEqual(again.Parts[i], form.Parts[i]) {
				t.Fatalf("part %d: %+v, want %+v", i, again.Parts[i], form.Parts[i])
			}
		}
		//: and the bytes are the fixed point.
		if second, err := c.Marshal(again); err != nil || !bytes.Equal(second, out) {
			t.Fatalf("second encode %q (%v), want %q", second, err, out)
		}
	})
}
