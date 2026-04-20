package logger_test

import (
	"bytes"
	"errors"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
)

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		handlerNil bool
		wantErrIs  error
	}{
		{"real handler produces logger", false, nil},
		{"nil handler returns HandlerNil", true, svclogger.HandlerNil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var h corelogger.Handler
			if !tc.handlerNil {
				h = mustNewText(t, &bytes.Buffer{}, level.Info)
			}
			lg, err := svclogger.New(h)
			if tc.wantErrIs == nil {
				if err != nil {
					t.Errorf("New returned err=%v", err)
				}
				if lg == nil {
					t.Errorf("New returned nil logger")
				}
				return
			}
			if !errors.Is(err, tc.wantErrIs) {
				t.Errorf("errors.Is(%v, HandlerNil) = false", err)
			}
			if code, _ := errs.CodeOf(err); code != svclogger.CodeHandlerNil {
				t.Errorf("CodeOf err = %d, want %d", code, svclogger.CodeHandlerNil)
			}
		})
	}
}

func TestBuild(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		nilLg   bool
		wantNil bool
	}{
		{"Build on nil Logger returns nil", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := svclogger.Build(nil, level.Info)
			if (b == nil) != tc.wantNil {
				t.Errorf("Build = %v, wantNil = %v", b, tc.wantNil)
			}
		})
	}
}

func TestLogAttrs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"LogAttrs on nil Logger silently drops"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: by contract this never panics — just confirm execution completes.
			svclogger.LogAttrs(t.Context(), nil, level.Info, "msg", nil)
		})
	}
}
