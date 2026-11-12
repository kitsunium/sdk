package http2_test

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// expectedRequestFields lists what the real client sends beyond the request's
// own fields: every request carries them.
func expectedRequestFields(authority, method, path string) []string {
	return []string{
		":authority=" + authority,
		":method=" + method,
		":path=" + path,
		":scheme=http",
	}
}

// TestGoClientOverDecoderAndEncoder drives the real http2 client against
// the frame layer and HPACK: every request decoded as the client sent it, every
// response read back through the client's own decoder. Repeating fields across
// requests exercises the dynamic table against the client's encoder.
func TestGoClientOverDecoderAndEncoder(t *testing.T) {
	seen := make(chan []string, 64)
	h := newH2cHarness(t, func(fields []string) *corenet.HTTP1ResponseValue {
		seen <- fields
		return &corenet.HTTP1ResponseValue{
			Status: 201,
			Header: []byte("Content-Type: text/plain\r\nX-Echo: one\r\nSet-Cookie: a=1\r\nCache-Control: no-store\r\n"),
			Body:   []byte("hello"),
		}
	})
	defer h.close()
	client, stop := h2cClient(t)
	defer stop()
	addr := h.ln.Addr().String()
	for i := range 8 {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/hello/"+strconv.Itoa(i)+"?q=1", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Custom-Header", "custom value")
		req.Header.Set("Accept", "text/plain")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		fields := <-seen
		h.reportFailures(t)
		path := "/hello/" + strconv.Itoa(i) + "?q=1"
		for _, want := range expectedRequestFields(addr, "GET", path) {
			parts := strings.SplitN(want, "=", 2)
			if !fieldsContain(fields, parts[0], parts[1]) {
				t.Fatalf("request %d: field %q not decoded in %v", i, want, fields)
			}
		}
		if !fieldsContain(fields, "x-custom-header", "custom value") || !fieldsContain(fields, "accept", "text/plain") {
			t.Fatalf("request %d: own fields missing in %v", i, fields)
		}
		if resp.ProtoMajor != 2 || resp.StatusCode != 201 || string(body) != "hello" {
			t.Fatalf("request %d: proto %d status %d body %q", i, resp.ProtoMajor, resp.StatusCode, body)
		}
		wants := map[string]string{"Content-Type": "text/plain", "X-Echo": "one", "Set-Cookie": "a=1", "Cache-Control": "no-store", "Content-Length": "5"}
		for k, v := range wants {
			if got := resp.Header.Get(k); got != v {
				t.Fatalf("request %d: response %s = %q, want %q (all %v)", i, k, got, v, resp.Header)
			}
		}
	}
}

// differentialField is one request or response field the fuzz composes.
type differentialField struct {
	name, value string
}

// composeFields reads names and values from a fuzz input, keeping only what the
// client and the codec agree is a field: a lowercase token and a printable
// ASCII value with no surrounding space.
func composeFields(in []byte, count int) []differentialField {
	fields := make([]differentialField, 0, count)
	for range count {
		if len(in) < 2 {
			break
		}
		n := 1 + int(in[0]%8)
		v := int(in[1] % 16)
		in = in[2:]
		name := make([]byte, 0, n)
		for _, c := range in[:min(n, len(in))] {
			name = append(name, 'a'+c%26)
		}
		in = in[min(n, len(in)):]
		value := make([]byte, 0, v)
		for _, c := range in[:min(v, len(in))] {
			value = append(value, '!'+c%93)
		}
		in = in[min(v, len(in)):]
		if len(name) == 0 {
			continue
		}
		fields = setField(fields, "x-"+string(name), string(value))
	}
	return fields
}

// setField keeps one value per name, the last one, as http.Header.Set does.
func setField(fields []differentialField, name, value string) []differentialField {
	for i := range fields {
		if fields[i].name == name {
			fields[i].value = value
			return fields
		}
	}
	return append(fields, differentialField{name, value})
}

// FuzzDifferential holds the client's encoder and the codec's decoder equal,
// and the codec's encoder and the client's decoder equal, over random fields:
// every field the client sends is decoded as it sent it, on a connection the
// fuzz keeps using so the dynamic table is exercised, and every response field
// the codec's Encoder wrote is read back by the client into the same value.
func FuzzDifferential(f *testing.F) {
	f.Add([]byte("seed-value-long-enough-for-several-fields"), uint16(200))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}, uint16(404))
	f.Add([]byte("a"), uint16(599))
	// work carries what the current iteration expects both ways.
	type work struct {
		req, res []differentialField
		status   int
	}
	var current work
	h := newH2cHarness(f, func(fields []string) *corenet.HTTP1ResponseValue {
		for _, c := range current.req {
			if !fieldsContain(fields, c.name, c.value) {
				panic("field not decoded: " + c.name)
			}
		}
		var hdr strings.Builder
		for _, c := range current.res {
			hdr.WriteString(c.name + ": " + c.value + "\r\n")
		}
		return &corenet.HTTP1ResponseValue{Status: current.status, Header: []byte(hdr.String())}
	})
	defer h.close()
	client, stop := h2cClient(f)
	defer stop()
	addr := h.ln.Addr().String()
	f.Fuzz(func(t *testing.T, in []byte, status uint16) {
		reqFields := composeFields(in, 4)
		resFields := composeFields(bytes.ReplaceAll(in, []byte{0}, []byte{1}), 3)
		statuses := [...]int{200, 201, 204, 301, 304, 400, 404, 500, 503}
		current = work{req: reqFields, res: resFields, status: statuses[int(status)%len(statuses)]}
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/f", nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range reqFields {
			req.Header.Set(c.name, c.value)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request with %v: %v", reqFields, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.ProtoMajor != 2 || resp.StatusCode != current.status {
			t.Fatalf("proto %d status %d, want %d", resp.ProtoMajor, resp.StatusCode, current.status)
		}
		for _, c := range resFields {
			if got := resp.Header.Get(c.name); got != c.value {
				t.Fatalf("response %s = %q, want %q (all %v)", c.name, got, c.value, resp.Header)
			}
		}
		h.reportFailures(t)
	})
}
