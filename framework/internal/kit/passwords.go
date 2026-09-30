// Package kit — the password policy of a store's fields.
package kit

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/kitsunium/sdk/framework/model"
)

// The password policy (ADR 0007 §2). A store declares the policy of one
// secret field that holds a password's hash:
//
//	var Passwords = Accounts.Passwords(func(a *Account) *string { return &a.Password },
//		kit.NotReused(10))
//
// and sets, changes and checks passwords through it (passwords_use.go). kit
// never keeps a password: the policy hashes with the SDK's password.Hash,
// and a write that puts anything but a PHC string into the field — any
// write: the product's, a workflow's — is refused. Only the policy reads a
// password's former hashes; Store.Former says when they changed, never what
// they were.

// The policy's bounds.
const (
	// defaultMinLength is NIST SP 800-63B-4's minimum for a password used
	// alone.
	defaultMinLength int = 15
	// minimumLength is the floor NIST allows, with a second factor.
	minimumLength int = 8
)

// PasswordPolicyService is the policy of one secret field that holds a password's
// hash: how long a password is at least, and how many former passwords it
// refuses. [StoreService.Passwords] declares it; Set, Change and Verify apply it.
type PasswordPolicyService[T any] struct {
	s    *StoreService[T]
	decl pos
	// member is the field, nil when the declaration is refused.
	member               *member
	minLength, notReused int
	// notCommon refuses the most common passwords (NotCommon).
	notCommon bool
}

// PasswordConfigurer configures a password policy: [MinLength], [NotReused],
// [NotCommon].
type PasswordConfigurer interface {
	passwordConfigure(o *passwordOptions)
}

type passwordOptions struct {
	minLength, notReused int
	reuseSet             bool
	notCommon            bool
}

type passwordOption func(o *passwordOptions)

// passwordConfigure sets the option on what it configures.
func (f passwordOption) passwordConfigure(o *passwordOptions) { f(o) }

// MinLength refuses a password shorter than n characters, counted as NIST
// counts them: one per Unicode code point. The default is 15, NIST's figure
// for a password used alone; 8 is the floor NIST allows with a second
// factor, and a smaller n is refused. There is no composition rule, ever:
// NIST says SHALL NOT.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func MinLength(n int) PasswordConfigurer {
	return passwordOption(func(o *passwordOptions) { o.minLength = n })
}

// NotReused refuses a password that the current hash, or any of the n
// former ones, verifies — n from 1 to 100. It is a compliance policy, off by
// default: PCI DSS asks for it, OWASP ASVS 4.0.3 asks most products not to
// impose it. kit keeps the field's last n hashes, so it implies history=n,
// and a tag whose history is smaller than n is refused.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func NotReused(n int) PasswordConfigurer {
	return passwordOption(func(o *passwordOptions) { o.notReused, o.reuseSet = n, true })
}

// NotCommon refuses a password among the ten thousand most common ones —
// the SDK's password.IsCommon, which compares them without regard to case —:
// the blocklist NIST SP 800-63B-4 requires a verifier to check, beyond the
// three thousand OWASP ASVS 5.0 (6.2.4) asks for. Every policy checks it (ADR
// 0007 §2: on by default once the SDK has the list); the option says so
// where a policy is declared. Beside NIST's fifteen characters the list has
// little left to refuse; beside a shorter MinLength, it matters.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func NotCommon() PasswordConfigurer {
	return passwordOption(func(o *passwordOptions) { o.notCommon = true })
}

// Passwords declares the password policy of one secret field of the store's
// entity; field names it — kit calls it on a zero entity to find which. The
// field then only ever holds the password's hash, a PHC string, or nothing
// before a first password; a write that puts anything else in it is
// refused. A field that is not secret refuses the start, as does a second
// policy on the same field.
//
//go:noinline
func (s *StoreService[T]) Passwords(field func(*T) *string, opts ...PasswordConfigurer) *PasswordPolicyService[T] {
	o := passwordOptions{minLength: defaultMinLength, notCommon: true}
	for _, opt := range opts {
		if opt != nil {
			opt.passwordConfigure(&o)
		}
	}
	p := &PasswordPolicyService[T]{s: s, decl: callerPos(), minLength: o.minLength, notReused: o.notReused, notCommon: o.notCommon}
	p.member = s.passwordField(field, p.decl)
	p.judge(o)
	s.passwords = append(s.passwords, p)
	return p
}

// passwordField finds the field field returns on a zero entity: a secret
// field of the entity itself, reached without a pointer. What it gets wrong
// is a declaration problem, at the policy's position.
func (s *StoreService[T]) passwordField(field func(*T) *string, at pos) *member {
	var zero T
	target := pointedBy(field, &zero)
	rv := reflect.ValueOf(&zero).Elem()
	name, found := fieldNamed(rv, target)
	if target == nil || !found {
		s.svc.problem(at, s.id, "passwords.no-field", "store", s.id)
		return nil
	}
	plan := s.plan()
	for i := range plan.members {
		m := &plan.members[i]
		if f := fieldAt(rv, m.path); f.IsValid() && f.CanAddr() && f.Addr().Interface() == any(target) {
			if m.tag.effective() == model.ClassSecret {
				return m
			}
			break
		}
	}
	s.svc.problem(at, s.id, "passwords.not-secret", "field", name, "store", s.id)
	return nil
}

// pointedBy is what field returns on zero: nil when it is nil or panics.
func pointedBy[T any](field func(*T) *string, zero *T) (p *string) {
	if field == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			p = nil
		}
	}()
	return field(zero)
}

// fieldNamed names the field of v — a struct, at any depth of its nested
// structs, never through a pointer — whose address is target.
func fieldNamed(v reflect.Value, target *string) (string, bool) {
	if v.Kind() != reflect.Struct || target == nil {
		return "", false
	}
	for i := range v.NumField() {
		f, sf := v.Field(i), v.Type().Field(i)
		if !sf.IsExported() {
			continue
		}
		if f.CanAddr() && f.Addr().Interface() == any(target) {
			return fieldLabel(v.Type(), sf.Name), true
		}
		if name, ok := fieldNamed(f, target); ok {
			return name, true
		}
	}
	return "", false
}

// judge says what the policy's options get wrong, and a second policy on
// its field.
func (p *PasswordPolicyService[T]) judge(o passwordOptions) {
	svc, at, id, m := p.s.svc, p.decl, p.s.id, p.member
	if o.minLength < minimumLength {
		svc.problem(at, id, "passwords.min-length", "min", o.minLength, "floor", minimumLength)
	}
	p.judgeReuse(o)
	if m != nil && p.s.policyOn(m.pointer) != nil {
		svc.problem(at, id, "passwords.twice", "field", m.field, "store", id)
	}
}

// judgeReuse refuses a reuse bound out of range, or longer than the history
// the field keeps.
func (p *PasswordPolicyService[T]) judgeReuse(o passwordOptions) {
	svc, at, id, m := p.s.svc, p.decl, p.s.id, p.member
	switch {
	case o.reuseSet && (o.notReused < 1 || o.notReused > historyLimit):
		svc.problem(at, id, "passwords.not-reused", "n", o.notReused, "max", historyLimit)
	case m != nil && m.tag.history > 0 && m.tag.history < o.notReused:
		svc.problem(at, id, "passwords.history-short", "field", m.field, "history", m.tag.history, "n", o.notReused)
	}
}

// policies are where the store's password policies are declared, in order:
// a module's store takes those of its own Go module only (foreignPolicies,
// module_check.go).
func (s *StoreService[T]) policies() []pos {
	out := make([]pos, len(s.passwords))
	for i, p := range s.passwords {
		out[i] = p.decl
	}
	return out
}

// policyOn is the store's policy on the field at pointer, nil when it has
// none yet.
func (s *StoreService[T]) policyOn(pointer string) *PasswordPolicyService[T] {
	for _, p := range s.passwords {
		if p.member != nil && p.member.pointer == pointer {
			return p
		}
	}
	return nil
}

// info describes the policy for the model.
func (p *PasswordPolicyService[T]) info(a *App) model.PasswordPolicy {
	out := model.PasswordPolicy{MinLength: p.minLength, NotReused: p.notReused, NotCommon: p.notCommon, Source: a.source(&p.decl)}
	if p.member != nil {
		out.Field = p.member.pointer
	}
	return out
}

// passwordHashes are the hashes v holds in the store's policies' fields, in
// the policies' order.
func (s *StoreService[T]) passwordHashes(v *T) []string {
	if len(s.passwords) == 0 {
		return nil
	}
	out := make([]string, len(s.passwords))
	for i, p := range s.passwords {
		out[i], _ = p.hashOf(v)
	}
	return out
}

// checkPasswords refuses a write that puts anything but a password's hash
// into one of the store's policies' fields. Nothing — no password yet, or
// an erased one — passes, and so does a value the write leaves as it was:
// before are the fields' hashes before the write, nil for a new record.
func (h *historied[T]) checkPasswords(before []string, v T) error {
	for i, p := range h.s.passwords {
		hash, _ := p.hashOf(&v)
		if hash == "" || (before != nil && before[i] == hash) || isPHC(hash) {
			continue
		}
		return Invalid(fmt.Sprintf("%s: %s holds a password's hash, never a password: set it through its password policy",
			h.s.name, p.member.pointer))
	}
	return nil
}

// hashOf is the hash v holds in the policy's field.
func (p *PasswordPolicyService[T]) hashOf(v *T) (string, bool) {
	if p.member == nil {
		return "", false
	}
	if f := fieldAt(reflect.ValueOf(v).Elem(), p.member.path); f.IsValid() && f.Kind() == reflect.String {
		return f.String(), true
	}
	return "", false
}

// setHash puts hash into v's field.
func (p *PasswordPolicyService[T]) setHash(v *T, hash string) {
	if f := fieldAt(reflect.ValueOf(v).Elem(), p.member.path); f.IsValid() && f.CanSet() {
		f.SetString(hash)
	}
}

// The PHC string format: $id[$v=version][$param=value(,param=value)*][$salt$hash].
var (
	phcID     = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9-]{1,32}$`) })
	phcParams = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^[a-z0-9-]{1,32}=[a-zA-Z0-9/+.-]+(,[a-z0-9-]{1,32}=[a-zA-Z0-9/+.-]+)*$`)
	})
	phcSalt = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-zA-Z0-9/+.-]+$`) })
	phcHash = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9+/]+$`) })
)

// isPHC reports whether s is written as a PHC string with a salt and a
// hash, as the SDK's password.Hash writes one. It judges the form only: the
// SDK verifies a hash.
func isPHC(s string) bool {
	fields := strings.Split(s, "$")
	if len(fields) < 4 || fields[0] != "" || !phcID().MatchString(fields[1]) {
		return false
	}
	rest := fields[2:]
	if strings.HasPrefix(rest[0], "v=") {
		rest = rest[1:]
	}
	if len(rest) == 3 && phcParams().MatchString(rest[0]) {
		rest = rest[1:]
	}
	return len(rest) == 2 && phcSalt().MatchString(rest[0]) && phcHash().MatchString(rest[1])
}
