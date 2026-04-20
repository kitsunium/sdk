package logger_test

import (
	"errors"
	"io"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
	"github.com/kitsunium/sdk/internal/service/logger/sink/console"
)

func TestNewHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		nilEnc  bool
		nilSink bool
		wantErr error
	}{
		{"happy path", false, false, nil},
		{"nil encoder is rejected", true, false, svclogger.EncoderNil},
		{"nil sink is rejected", false, true, svclogger.SinkRequired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var enc encoder.Encoder
			var sink corelogger.Sink
			if !tc.nilEnc {
				enc = encoder.NewText(clock.System)
			}
			if !tc.nilSink {
				s, err := console.New(io.Discard)
				if err != nil {
					t.Fatalf("console.New err = %v", err)
				}
				sink = s
			}
			got, err := svclogger.NewHandler(enc, sink, level.Info)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && got == nil {
				t.Error("expected non-nil handler on happy path")
			}
		})
	}
}
