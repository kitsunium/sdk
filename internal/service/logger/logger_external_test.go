package logger_test

import (
	"bytes"
	"errors"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/level"
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
