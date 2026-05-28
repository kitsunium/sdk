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
				t.Errorf("CodeOf err = %v, want %v", code, svclogger.CodeHandlerNil)
			}
		})
	}
}

func TestBuild(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		wantNil bool
	}{
		{"Build on nil Logger returns nil", true},
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
		name    string
		nilLog  bool
		message string
		callLvl level.Level
		minLvl  level.Level
		want    bool
	}{
		{"LogAttrs on nil Logger silently drops without panicking", true, "msg", level.Info, level.Info, false},
		{"LogAttrs on real Logger above threshold emits the line", false, "emitted", level.Warn, level.Info, true},
		{"LogAttrs on real Logger below threshold emits nothing", false, "quiet", level.Debug, level.Info, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: nil-logger arm asserts the silent-drop contract: no panic, no write.
			if tc.nilLog {
				panicked := false
				func() {
					defer func() {
						if r := recover(); r != nil {
							panicked = true
						}
					}()
					svclogger.LogAttrs(t.Context(), nil, tc.callLvl, tc.message, nil)
				}()
				if panicked {
					t.Error("LogAttrs panicked on nil Logger; contract requires silent drop")
				}
				return
			}
			//: real-logger arm exercises the happy path that delegates to the
			//: concrete loggerImpl and honours the handler's Enabled gate.
			var buf bytes.Buffer
			lg, err := svclogger.New(mustNewText(t, &buf, tc.minLvl))
			if err != nil {
				t.Fatalf("New returned err: %v", err)
			}
			attrs := []corelogger.AttrValue{{Key: "k", Value: corelogger.StringValue("v")}}
			svclogger.LogAttrs(t.Context(), lg, tc.callLvl, tc.message, attrs)
			if got := bytes.Contains(buf.Bytes(), []byte(tc.message)); got != tc.want {
				t.Errorf("LogAttrs emitted=%v, want %v (buf=%q)", got, tc.want, buf.String())
			}
		})
	}
}
