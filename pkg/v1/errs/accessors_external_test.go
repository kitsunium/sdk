package errs_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"
	"github.com/kitsunium/sdk/pkg/v1/mail"
)

// TestV1ErrsEndToEnd proves that a real failure surfaced by pkg/v1/logger
// flows through pkg/v1/errs accessors with the expected values (V-OI-1).
func TestV1ErrsEndToEnd(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"NewText(Config{}) surfaces WriterRequired through v1 accessors"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := logger.NewText(logger.Config{})
			if err == nil {
				t.Fatal("expected NewText error, got nil")
			}
			//: 0x01_01_00_01 = 1.1.0.1 (pkg/v1/logger WriterRequired under ADR 0005).
			const writerRequired errs.Code = 0x01_01_00_01
			if !errs.HasCode(err, writerRequired) {
				t.Errorf("HasCode(err, 1.1.0.1) = false")
			}
			if !errs.HasReason(err, "WRITER_REQUIRED") {
				t.Errorf("HasReason(err, WRITER_REQUIRED) = false")
			}
			if code, ok := errs.CodeOf(err); !ok || code != writerRequired {
				t.Errorf("CodeOf = (%s, %v)", code, ok)
			}
			if reason, ok := errs.ReasonOf(err); !ok || reason != "WRITER_REQUIRED" {
				t.Errorf("ReasonOf = (%q, %v)", reason, ok)
			}
			if got := errs.PublicOf(err); got != "Logger config requires an explicit writer" {
				t.Errorf("PublicOf = %q", got)
			}
			if got := errs.PrivateOf(err); got == "" {
				t.Errorf("PrivateOf = empty")
			}
			//: Layer byte (second octet) of 0x01_01_00_01 is 1; composable via
			//: errs.CodeOf(err).Layer() now that the typed Code is the single API.
			if code, ok := errs.CodeOf(err); !ok || code.Layer() != 1 {
				t.Errorf("CodeOf(err).Layer() = %d (ok=%v)", code.Layer(), ok)
			}
			if errs.HTTPStatusOf(err) != 500 {
				t.Errorf("HTTPStatusOf = %d", errs.HTTPStatusOf(err))
			}
			if errs.ExitCodeOf(err) != 70 {
				t.Errorf("ExitCodeOf = %d", errs.ExitCodeOf(err))
			}
		})
	}
}

func TestV1ErrsOnStdlibError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"stdlib error yields defaults and false flags"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := errors.New("plain stdlib")
			if _, ok := errs.CodeOf(err); ok {
				t.Error("CodeOf should return ok=false for stdlib error")
			}
			if errs.HasCode(err, 0x01_01_00_01) {
				t.Error("HasCode should be false for stdlib error")
			}
			if errs.HTTPStatusOf(err) != 500 {
				t.Errorf("HTTPStatusOf = %d", errs.HTTPStatusOf(err))
			}
		})
	}
}

// TestHasAnyCode_AndHasAnyReason cover the variadic OR helpers added
// alongside HasCode / HasReason — common shape for retry / circuit
// breaker / metric routing on a SET of codes rather than a single one.
func TestHasAnyCode_AndHasAnyReason(t *testing.T) {
	t.Parallel()

	//: force a real failure path to obtain a typed *errs.Error with a
	//: known Code + Reason. NewText(Config{Writer:nil}) returns
	//: WriterRequired (1.1.0.1) per ADR 0005.
	_, err := logger.NewText(logger.Config{})
	if err == nil {
		t.Fatal("setup: NewText with nil Writer must fail")
	}

	cases := []struct {
		name    string
		codes   []errs.Code
		reasons []string
		want    bool
	}{
		{"single match in code list", []errs.Code{0x01_01_00_01}, nil, true},
		{"match somewhere in code list", []errs.Code{0x99_99_99_99, 0x01_01_00_01, 0x00_00_00_42}, nil, true},
		{"no match in code list", []errs.Code{0x99_99_99_99}, nil, false},
		{"empty code list", nil, nil, false},
		{"single match in reason list", nil, []string{"WRITER_REQUIRED"}, true},
		{"match somewhere in reason list", nil, []string{"UNKNOWN", "WRITER_REQUIRED", "OTHER"}, true},
		{"no match in reason list", nil, []string{"UNKNOWN_THING"}, false},
		{"empty reason list", nil, []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if len(tc.codes) > 0 || tc.codes == nil && len(tc.reasons) == 0 {
				got := errs.HasAnyCode(err, tc.codes...)
				if got != tc.want {
					t.Errorf("HasAnyCode(%v) = %v want %v", tc.codes, got, tc.want)
				}
			}
			if len(tc.reasons) > 0 || tc.codes == nil && tc.reasons != nil {
				got := errs.HasAnyReason(err, tc.reasons...)
				if got != tc.want {
					t.Errorf("HasAnyReason(%v) = %v want %v", tc.reasons, got, tc.want)
				}
			}
		})
	}
}

// TestFieldsOfReadsTheClauseAnEmitterAttached reads, with the public names
// alone, the clause mail.ParseURL attaches to a refusal — what a framework's
// settings page shows beside a setting it could not use — and pins the two
// promises the FieldsOf doc makes: the fields come oldest cause first, newest
// wrapper last, and the password in the URL is in no field and in no message.
func TestFieldsOfReadsTheClauseAnEmitterAttached(t *testing.T) {
	t.Parallel()
	const password = "correct-horse-battery"
	type tc struct {
		name string
		raw  string
		want string
	}
	tests := []tc{
		{
			"an unknown tls mode",
			"smtp://camille:" + password + "@mail.example.com:587?tls=bogus",
			"has a tls parameter that is not starttls, implicit or none",
		},
		{
			"a port out of range",
			"smtp://camille:" + password + "@mail.example.com:99999",
			"has a port that is not a number from 1 to 65535",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := mail.ParseURL(c.raw)
		if !errors.Is(err, mail.InvalidURL) {
			t.Fatalf("ParseURL = %v, want InvalidURL", err)
		}
		//: a caller adds its own clause on the way up; it comes last.
		wrapped := errs.Wrap(err, errs.WrapParams{Code: 0x40_01_01_01}, errs.String("setting", "KIT_SMTP_URL"))
		fields := errs.FieldsOf(wrapped)
		keys := make([]string, 0, len(fields))
		problem := ""
		for _, field := range fields {
			keys = append(keys, field.Key())
			if field.Key() == "problem" {
				problem = field.StringValue()
			}
			if strings.Contains(field.StringValue(), password) {
				t.Errorf("field %q carries the password", field.Key())
			}
		}
		if !slices.Equal(keys, []string{"problem", "setting"}) {
			t.Fatalf("FieldsOf keys = %v, want [problem setting]", keys)
		}
		if problem != c.want {
			t.Fatalf("problem = %q, want %q", problem, c.want)
		}
		for _, text := range []string{wrapped.Error(), errs.PublicOf(wrapped), errs.PrivateOf(wrapped)} {
			if strings.Contains(text, password) {
				t.Errorf("a rendering carries the password: %q", text)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFieldsOfWithoutAnSDKError pins the absence arm through the facade: no
// *errs.Error on the chain, no fields — nil, not an empty slice.
func TestFieldsOfWithoutAnSDKError(t *testing.T) {
	t.Parallel()
	for _, err := range []error{nil, errors.New("plain")} {
		if got := errs.FieldsOf(err); got != nil {
			t.Errorf("FieldsOf(%v) = %v, want nil", err, got)
		}
	}
}
