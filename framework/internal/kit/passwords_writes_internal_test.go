package kit

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// What goes into a policy's field, and what a declaration gets wrong (ADR
// 0007 §2): only a password's hash, a legacy value left as it was, and every
// problem of a declaration at once, at its position.

// Only a password's hash goes into a policy's field: a plain value is
// refused, whoever writes it, and nothing — no password yet — passes.
func TestAPlainValueIsRefused(t *testing.T) {
	useFakeHashing(t)
	l := startLocks(t, "locks-plain", []PasswordConfigurer{NotReused(2)})
	ctx := t.Context()
	for _, err := range []error{
		l.accounts.Insert(ctx, lockAccount{ID: "a1", Password: "hunter2"}),
		l.accounts.Put(ctx, lockAccount{ID: "a1", Password: "hunter2"}),
	} {
		if !isInvalid(err) || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("a plain password: %v", err)
		}
	}
	must(t, l.accounts.Insert(ctx, lockAccount{ID: "a1"}))
	if _, err := l.accounts.Update(ctx, "a1", func(a *lockAccount) error { a.Password = "hunter2"; return nil }); !isInvalid(err) {
		t.Errorf("an update to a plain password: %v", err)
	}
	must(t, l.policy.Set(ctx, "a1", pw("one")))
	if _, err := l.accounts.Update(ctx, "a1", func(a *lockAccount) error { a.Password = ""; return nil }); err != nil {
		t.Errorf("clearing the password: %v", err)
	}
}

// A value written before the policy existed is left as it was: a write
// that does not change it passes, one that puts another plain value is
// refused.
func TestALegacyValueIsLeftAsItWas(t *testing.T) {
	useFakeHashing(t)
	dir := t.TempDir()
	before := NewService("locks-legacy", "")
	plain := before.Store("accounts", func(a lockAccount) string { return a.ID })
	app := NewApp("legacy", before).With(DataDir(dir), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	must(t, app.Start(t.Context()))
	must(t, plain.Insert(t.Context(), lockAccount{ID: "a1", Password: "legacy"}))
	must(t, app.Stop(context.Background()))
	l := startLocks(t, "locks-legacy", nil, DataDir(dir))
	ctx := t.Context()
	if _, err := l.accounts.Update(ctx, "a1", func(a *lockAccount) error { a.Logins++; return nil }); err != nil {
		t.Errorf("a write that leaves the legacy value: %v", err)
	}
	if err := l.accounts.Put(ctx, lockAccount{ID: "a1", Password: "other"}); !isInvalid(err) {
		t.Errorf("another plain value: %v", err)
	}
}

// What a declaration gets wrong refuses the start, every problem at once
// at the policy's position.
func TestABadPolicyRefusesTheStart(t *testing.T) {
	type twoFields struct {
		ID     string `json:"id"`
		Plain  string `json:"plain"`
		Hash   string `json:"hash" kit:"secret,history=1"`
		Nested struct {
			Hash string `json:"hash" kit:"secret"`
		} `json:"nested"`
	}
	svc := NewService("locks-bad", "")
	st := svc.Store("accounts", func(a twoFields) string { return a.ID })
	st.Passwords(func(a *twoFields) *string { return &a.Plain })
	st.Passwords(func(a *twoFields) *string { return &a.Hash }, MinLength(4), NotReused(2))
	st.Passwords(func(a *twoFields) *string { return &a.Hash }, NotReused(101))
	st.Passwords(func(*twoFields) *string { return nil })
	st.Passwords(func(a *twoFields) *string { return &a.Nested.Hash })
	err := NewApp("bad", svc).With(InMemory(), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard)).Start(t.Context())
	var d *DiagnosticsError
	if !errors.As(err, &d) {
		t.Fatalf("the start: %v", err)
	}
	said := err.Error()
	for _, want := range []string{
		"is not tagged kit:\"secret\"", "MinLength(4)", "keeps fewer former values than NotReused(2)",
		"NotReused(101)", "has two password policies", "names no field of the entity", "passwords_writes_internal_test.go",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the start does not say %q: %s", want, said)
		}
	}
	if strings.Count(said, "\n") != 6 {
		t.Errorf("want six problems: %s", said)
	}
}
