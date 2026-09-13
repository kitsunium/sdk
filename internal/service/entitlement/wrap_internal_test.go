package entitlement

import (
	"crypto/ed25519"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestNoParticularReachesThePublicSentence is the assertion the conversion to
// errs.Wrap exists for, and it runs in BOTH directions.
//
// The mechanical half of that conversion — s/fmt.Errorf/classify/ — passes
// every other test in this package while putting a url, a host, a key
// identifier or a filesystem path straight back into the half documented safe
// for a response body. What the split is about is where each fact landed: the
// public sentence says WHAT happened, the fields say WHERE and WHY.
//
// Leaking a particular and losing it are both defects, and only one of them is
// the one everybody remembers, so each row asserts the particular is absent
// from err.Error() AND present in particulars(err).
func TestNoParticularReachesThePublicSentence(t *testing.T) {
	t.Parallel()

	vendorPub, _, keyErr := ed25519.GenerateKey(nil)
	//: A failure here is an environment problem, not a test outcome.
	if keyErr != nil {
		t.Fatalf("generating vendor key: %v", keyErr)
	}

	tests := []struct {
		name string
		// build produces the refusal under test.
		build func(t *testing.T) error
		// particular is the fact that must be in the fields and out of the
		// sentence.
		particular string
		reason     string
	}{
		{
			name: "the roster fetch names its url",
			build: func(t *testing.T) error {
				t.Helper()
				svc := NewServiceWithOrigins(stubGet(func(string) (*http.Response, error) {
					return nil, errors.New("dial refused")
				}), stubIdentity{}, vendorPub, nil)
				_, err := svc.fetch("https://roster.example.test/bundle.json")
				return err
			},
			particular: "roster.example.test",
			reason:     "a publication endpoint is infrastructure, and a response body is not where it belongs",
		},
		{
			name: "the roster fetch names what the transport said",
			build: func(t *testing.T) error {
				t.Helper()
				svc := NewServiceWithOrigins(stubGet(func(string) (*http.Response, error) {
					return nil, errors.New("dial refused")
				}), stubIdentity{}, vendorPub, nil)
				_, err := svc.fetch("https://roster.example.test/bundle.json")
				return err
			},
			particular: "dial refused",
			reason:     "the operator acts on the syscall's own words, and nothing else can restate them",
		},
		{
			name: "the cache read names its path",
			build: func(t *testing.T) error {
				t.Helper()
				_, err := readCappedFile(t.TempDir() + "/absent.json")
				return err
			},
			particular: "absent.json",
			reason:     "a path on somebody's disk says where their cache lives",
		},
		{
			name: "the key selection names the kid",
			build: func(*testing.T) error {
				return usableRSAKey(nil, "a-published-kid")
			},
			particular: "a-published-kid",
			reason:     "a key identifier is the issuer's internal naming, not the caller's business",
		},
		{
			name: "the subject match names the subject",
			build: func(t *testing.T) error {
				t.Helper()
				now := time.Now()
				roster := &coreent.RosterValue{
					IssuedAt:  now.Add(-time.Hour),
					ExpiresAt: now.Add(time.Hour),
					Subjects: map[string]coreent.SubjectValue{
						"a-licensed-subject": {Fingerprint: "SHA256:published"},
					},
				}
				svc := NewServiceWithGetter(stubGet(func(string) (*http.Response, error) {
					return nil, errors.New("unused")
				}), stubIdentity{fingerprint: "SHA256:local"}, vendorPub, nil)
				_, err := svc.matchSubject(roster, "a-licensed-subject", now)
				return err
			},
			particular: "a-licensed-subject",
			reason:     "the subject is the identifier the vendor's roster keys on",
		},
		{
			name: "the token url check names the host it refused",
			build: func(*testing.T) error {
				_, err := checkTokenURL("https://not-github.example.test/token")
				return err
			},
			particular: "not-github.example.test",
			reason:     "the host the runner named is what an operator has to look at, and is not wire-safe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.build(t)
			if err == nil {
				t.Fatalf("build() error = nil, want a refusal (%s)", tt.reason)
			}
			//: The half documented safe for a response body must not carry it.
			if strings.Contains(err.Error(), tt.particular) {
				t.Errorf("err.Error() = %q, want %q OUT of it (%s)", err, tt.particular, tt.reason)
			}
			//: ...and the diagnostic half must, or the split lost it instead
			//: of moving it, which is the other defect and the quieter one.
			if diagnosis := particulars(err); !strings.Contains(diagnosis, tt.particular) {
				t.Errorf("particulars(err) = %q, want %q IN it (%s)", diagnosis, tt.particular, tt.reason)
			}
		})
	}
}

// Test_annotate pins the guard, which is not defensive.
//
// coreent.Identity is a PORT. A consumer implementing it with a plain
// errors.New — which nothing forbids and which is the obvious first
// implementation — hands ciContext a device refusal carrying no *errs.Error.
// errs.Wrap has no spelling for "attach a field, decide nothing": zero
// WrapParams over such a cause fails validateDefineArgs and returns
// CodeInvalidWrapParams, "internal wrap failure", with the consumer's own
// refusal demoted to a cause nobody prints. So annotate returns it untouched
// and drops the field instead.
func Test_annotate(t *testing.T) {
	t.Parallel()

	plain := errors.New("a port implementation's own refusal")

	tests := []struct {
		name string
		// err is what a caller hands in.
		err error
		// wantSame is whether the result must be the identical error value.
		wantSame bool
		// wantField is whether the annotation must be readable back.
		wantField bool
		reason    string
	}{
		{
			name:      "an SDK error keeps its identity and gains the field",
			err:       coreent.ErrNoLicense,
			wantField: true,
			reason:    "origin wins: code, reason and both messages stay the cause's",
		},
		{
			name:     "a plain error is returned untouched",
			err:      plain,
			wantSame: true,
			reason:   "the alternative is replacing a consumer's refusal with INVALID_WRAP_PARAMS",
		},
		{
			name:     "a nil error stays nil",
			err:      nil,
			wantSame: true,
			reason:   "annotating a success would invent a failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := annotate(tt.err, errs.String("ci_refusal", "the seat was refused too"))
			//: Identity first: everything else is worthless if the caller's
			//: own error was replaced.
			if tt.wantSame && !errors.Is(got, tt.err) && got != tt.err {
				t.Fatalf("annotate() = %v, want the input back unchanged (%s)", got, tt.reason)
			}
			//: A plain input must come back byte-identical, not merely
			//: matchable: INVALID_WRAP_PARAMS keeps the cause and still
			//: destroys what the caller prints.
			if tt.wantSame && got != tt.err {
				t.Errorf("annotate() = %v, want the identical value (%s)", got, tt.reason)
			}
			if !tt.wantField {
				return
			}
			if !errors.Is(got, tt.err) {
				t.Errorf("annotate() = %v, want errors.Is(%v) (%s)", got, tt.err, tt.reason)
			}
			if field := probeField(got, "ci_refusal"); field == "" {
				t.Errorf("annotate() carries no ci_refusal field (%s)", tt.reason)
			}
		})
	}
}

// Test_classify pins the two properties every converted call site rests on:
// the cause survives errors.Is, and a nil sentinel does not take the process
// down on the path that is already reporting a failure.
func Test_classify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// sentinel is what the call site names, possibly nothing.
		sentinel *errs.Error
		// wantCode is the code the result must carry.
		wantCode errs.Code
		reason   string
	}{
		{
			name:     "the sentinel's identity is read off it, never restated",
			sentinel: coreent.ErrRosterUnreachable,
			wantCode: coreent.CodeRosterUnreachable,
			reason:   "sixty call sites naming one sentinel must not be sixty copies of four strings",
		},
		{
			name:     "a nil sentinel degrades rather than panics",
			sentinel: nil,
			wantCode: errs.CodeInvalidWrapParams,
			reason:   "only a bug here produces one, and a panic on the reporting path is the worst outcome",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cause := fs.ErrNotExist
			got := classify(tt.sentinel, cause, errs.String("stage", "read"))
			//: The stdlib cause must remain matchable, whatever we said about
			//: it: errors.Is(err, fs.ErrNotExist) is the thing a wrapper
			//: destroys most often.
			if !errors.Is(got, cause) {
				t.Errorf("classify() = %v, want errors.Is(fs.ErrNotExist) (%s)", got, tt.reason)
			}
			code, ok := errs.CodeOf(got)
			if !ok || code != tt.wantCode {
				t.Errorf("classify() code = %v/%v, want %v (%s)", code, ok, tt.wantCode, tt.reason)
			}
		})
	}
}

// Test_diagnose pins what the one LOG line in this package gets to say.
//
// rememberRoster reports a cache it could not replace, and after the
// conversion err.Error() is the wire-safe sentence alone — no directory, no
// syscall text. A log an operator owns is not a wire, so diagnose puts both
// back underneath it. Without this the line would say only that something
// could not be written.
func Test_diagnose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// err is what the log line is handed.
		err error
		// want are fragments the rendering must carry.
		want   []string
		reason string
	}{
		{
			name:   "a nil error renders as nothing",
			err:    nil,
			reason: "a caller renders it unconditionally",
		},
		{
			name:   "the private sentence, the fields and the cause, in that order",
			err:    classify(CacheUnwritable, fs.ErrNotExist, errs.String("step", "mkdir"), errs.String("dir", "/var/cache/thing")),
			want:   []string{"service/entitlement:", "step=mkdir", "dir=/var/cache/thing", "cause=file does not exist"},
			reason: "the operator needs the step, the directory and what the filesystem said",
		},
		{
			name:   "our own Public is never quoted back",
			err:    refuse(coreent.ErrRosterUnreachable, errs.String("stage", "fetch")),
			reason: "it is the sentence printed one line above, and it is not news",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := diagnose(tt.err)
			//: A nil error renders empty so the call site needs no branch.
			if tt.err == nil {
				if got != "" {
					t.Errorf("diagnose(nil) = %q, want %q (%s)", got, "", tt.reason)
				}
				return
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("diagnose() = %q, want it to carry %q (%s)", got, want, tt.reason)
				}
			}
			//: Never the public half: foreignCause walks past our own errors
			//: precisely so the line below is not the line above.
			if strings.Contains(got, "cause="+errs.PublicOf(tt.err)) {
				t.Errorf("diagnose() = %q, want our own Public OUT of the cause (%s)", got, tt.reason)
			}
		})
	}
}
