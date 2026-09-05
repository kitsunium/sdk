// Package tlsid — white-box tests for the two loading helpers. Both encode the
// distinction the whole package exists for: "the caller did not configure this"
// must never be confused with "the caller configured it and it is broken".
package tlsid

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fieldValue returns the StringValue of the first field keyed key, or "".
func fieldValue(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		//: the first match wins; fields merge along the wrap chain.
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}

// Test_readMaterial pins the three outcomes an optional PEM path can have.
//
// The empty-file case is the one worth isolating. A file that exists but holds
// nothing reads without error, and returning its zero bytes would reach core as
// "no bundle supplied" — which means "use the platform trust store". An operator
// who truncated a CA file would silently widen trust rather than fail to start.
func Test_readMaterial(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	present := filepath.Join(dir, "present.pem")
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(present, []byte("-----BEGIN CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("writing the empty fixture: %v", err)
	}

	type tc struct {
		name    string
		path    string
		field   string
		wantNil bool
		wantErr bool
	}
	tests := []tc{
		{name: "an unconfigured path", path: "", field: "roots_file", wantNil: true},
		{name: "a readable file", path: present, field: "roots_file"},
		{name: "a file that does not exist", path: filepath.Join(dir, "absent.pem"), field: "roots_file", wantErr: true},
		{name: "an empty file", path: empty, field: "roots_file", wantErr: true},
		{name: "a directory where a file belongs", path: dir, field: "cert_file", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := readMaterial(c.path, c.field)
		if c.wantErr {
			if !errs.HasCode(err, corenet.CodeTLSMaterialInvalid) {
				t.Fatalf("readMaterial(%s) = %v, want TLS_MATERIAL_INVALID", c.name, err)
			}
			//: a refusal must hand back no bytes, or a caller checking only the
			//: slice would parse a half-read file.
			if got != nil {
				t.Errorf("readMaterial(%s) returned %d bytes beside the error", c.name, len(got))
			}
			//: the field names WHICH of the four paths was wrong.
			if fv := fieldValue(err, "field"); fv != c.field {
				t.Errorf("the field annotation is %q, want %q", fv, c.field)
			}
			//: the path is operator-supplied configuration, not a secret, so it
			//: is echoed to make the message actionable.
			if pv := fieldValue(err, "path"); pv != c.path {
				t.Errorf("the path annotation is %q, want %q", pv, c.path)
			}
			return
		}
		if err != nil {
			t.Fatalf("readMaterial(%s) = %v, want nil", c.name, err)
		}
		//: nil means "not configured", which core reads as "no material"; an
		//: empty non-nil slice would mean the same thing and must not occur.
		if (got == nil) != c.wantNil {
			t.Errorf("readMaterial(%s) returned nil = %v, want %v", c.name, got == nil, c.wantNil)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wrapMaterial pins that a loading failure keeps the domain's own code
// whatever the filesystem returned, and that the cause rides as a field rather
// than becoming the origin — an *errs.Error from somewhere else would otherwise
// hijack the sentinel a caller matches on.
func Test_wrapMaterial(t *testing.T) {
	t.Parallel()
	type tc struct {
		name         string
		cause        error
		field        string
		path         string
		wantHasCause bool
	}
	tests := []tc{
		{name: "an OS read failure", cause: os.ErrNotExist, field: "roots_file", path: "/etc/ca.pem", wantHasCause: true},
		{name: "a permission denial", cause: os.ErrPermission, field: "key_file", path: "/etc/key.pem", wantHasCause: true},
		//: a nil cause is the "read fine, carried nothing" case, which has no
		//: OS error to quote.
		{name: "an empty file", cause: nil, field: "cert_file", path: "/etc/cert.pem"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := wrapMaterial(c.cause, c.field, c.path)

		if !errs.HasCode(err, corenet.CodeTLSMaterialInvalid) {
			t.Fatalf("wrapMaterial = %v, want TLS_MATERIAL_INVALID", err)
		}
		if got := fieldValue(err, "field"); got != c.field {
			t.Errorf("the field annotation is %q, want %q", got, c.field)
		}
		if got := fieldValue(err, "path"); got != c.path {
			t.Errorf("the path annotation is %q, want %q", got, c.path)
		}
		//: the OS error explains WHY the read failed, which the sentinel alone
		//: cannot say.
		hasCause := fieldValue(err, "cause") != ""
		if hasCause != c.wantHasCause {
			t.Errorf("a cause field is present = %v, want %v", hasCause, c.wantHasCause)
		}
		//: the public message stays the domain's own; it must not become the
		//: filesystem's.
		if c.cause != nil && strings.Contains(errs.PublicOf(err), c.cause.Error()) {
			t.Errorf("the public message leaked the OS error: %q", errs.PublicOf(err))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
