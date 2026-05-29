package writer_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
)

func TestNewCredentialValue(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		ak, sk, st string
	}
	tests := []tc{
		{"long-lived keys, no session", "AKIA1", "sk1", ""},
		{"assumed role with session", "AKIA2", "sk2", "tok2"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := writer.NewCredentialValue(c.ak, c.sk, c.st)
		//: every accessor must round-trip the SigV4 material verbatim.
		if got.AccessKeyID() != c.ak || got.SecretAccessKey() != c.sk || got.SessionToken() != c.st {
			t.Errorf("%s: got (%q,%q,%q)", c.name, got.AccessKeyID(), got.SecretAccessKey(), got.SessionToken())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestCredentialValue_AccessKeyID(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	tests := []tc{{"populated", "AKIA-x"}, {"empty", ""}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the access key ID must surface exactly as supplied.
		if got := writer.NewCredentialValue(c.in, "s", "t").AccessKeyID(); got != c.in {
			t.Errorf("%s: AccessKeyID()=%q want %q", c.name, got, c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestCredentialValue_SecretAccessKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	tests := []tc{{"populated", "secret-x"}, {"empty", ""}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the secret must surface exactly as supplied (logging is the caller's risk).
		if got := writer.NewCredentialValue("a", c.in, "t").SecretAccessKey(); got != c.in {
			t.Errorf("%s: SecretAccessKey()=%q want %q", c.name, got, c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestCredentialValue_SessionToken(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	tests := []tc{{"populated", "session-x"}, {"empty (long-lived keys)", ""}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the session token must surface exactly as supplied.
		if got := writer.NewCredentialValue("a", "s", c.in).SessionToken(); got != c.in {
			t.Errorf("%s: SessionToken()=%q want %q", c.name, got, c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestCredentialValue_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   writer.CredentialValue
	}
	tests := []tc{
		{"populated redacts", writer.NewCredentialValue("AKIA", "secret", "session")},
		{"zero value redacts", writer.CredentialValue{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: String must never surface any secret material.
		if got := c.in.String(); got != "<redacted>" {
			t.Errorf("%s: String()=%q want <redacted>", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
