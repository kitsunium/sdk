package errs_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"
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
			const writerRequired = 0x01_01_00_01
			if !errs.HasCode(err, writerRequired) {
				t.Errorf("HasCode(err, 1.1.0.1) = false")
			}
			if !errs.HasReason(err, "WRITER_REQUIRED") {
				t.Errorf("HasReason(err, WRITER_REQUIRED) = false")
			}
			if code, ok := errs.CodeOf(err); !ok || code != int(uint32(writerRequired)) {
				t.Errorf("CodeOf = (%d, %v)", code, ok)
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
			//: Layer byte (second octet) of 0x01_01_00_01 is 1, not 4 (old flat
			//: scheme computed layer as code/1000 = 4).
			if errs.LayerOf(err) != 1 {
				t.Errorf("LayerOf = %d", errs.LayerOf(err))
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
