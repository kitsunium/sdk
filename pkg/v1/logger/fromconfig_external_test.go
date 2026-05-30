package logger_test

import (
	"bytes"
	"context"
	"sync"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	corewriter "github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"

	// Activate the JSON codec so FromConfig can decode a JSON topology. Test
	// code is exempt from the dep-light rule that bars pkg/v1/logger itself
	// from blank-importing a service codec.
	_ "github.com/kitsunium/sdk/internal/service/codec/json"
)

// topologyInvalidCode is the dotted-quad of pkg/v1/logger.TopologyInvalid
// (1.1.0.4), matched via the read-only errs accessor.
const topologyInvalidCode errs.Code = 0x01_01_00_04

// bufferSink is a minimal Sink that records every payload into a shared buffer,
// letting the happy-path test prove FromConfig wired a working logger.
type bufferSink struct {
	mu  sync.Mutex
	buf *bytes.Buffer
}

// Write appends p to the shared buffer and reports the byte count.
func (s *bufferSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: record the payload so the test can assert the logger emitted.
	return s.buf.Write(p)
}

// Flush is a no-op; the buffer needs no flushing.
func (s *bufferSink) Flush(_ context.Context) error {
	//: nothing buffered downstream.
	return nil
}

// Close is a no-op; the buffer owns no resource.
func (s *bufferSink) Close() error {
	//: nothing to release.
	return nil
}

// decoderTestFactory implements writer.Decoder; it builds a bufferSink and
// records that Decode was the path taken.
type decoderTestFactory struct {
	sink         *bufferSink
	decoderUsed  *bool
	registeredAs corewriter.Name
}

// compile-time proof the fakes satisfy the contracts they stand in for.
var (
	_ corewriter.Factory = (*decoderTestFactory)(nil)
	_ corewriter.Decoder = (*decoderTestFactory)(nil)
	_ corewriter.Factory = (*plainTestFactory)(nil)
)

// Name reports the registration key.
func (f *decoderTestFactory) Name() corewriter.Name {
	//: the fixed key under which this fake registers.
	return f.registeredAs
}

// Open returns the shared bufferSink regardless of cfg.
func (f *decoderTestFactory) Open(_ corewriter.Config) (corelogger.Sink, error) {
	//: hand back the shared sink so the test can read what was written.
	return f.sink, nil
}

// Decode flags that the decoder path ran and echoes the map.
func (f *decoderTestFactory) Decode(raw map[string]any) (corewriter.Config, error) {
	//: mark the decoder branch so the test can assert it was chosen.
	*f.decoderUsed = true
	//: echo the map back as the opaque Config.
	return raw, nil
}

// plainTestFactory does NOT implement writer.Decoder; FromConfig must fall back
// to the default mapping (the raw map handed straight to Open).
type plainTestFactory struct {
	sink         *bufferSink
	registeredAs corewriter.Name
}

// Name reports the registration key.
func (f *plainTestFactory) Name() corewriter.Name {
	//: the fixed key under which this fake registers.
	return f.registeredAs
}

// Open returns the shared bufferSink; the default mapping passes a map[string]any.
func (f *plainTestFactory) Open(_ corewriter.Config) (corelogger.Sink, error) {
	//: hand back the shared sink so the test can read what was written.
	return f.sink, nil
}

// shared test fakes, registered exactly once at package load to avoid the
// registry's duplicate-registration panic across subtests.
var (
	decoderUsedFlag bool
	decoderSink     = &bufferSink{buf: &bytes.Buffer{}}
	plainSink       = &bufferSink{buf: &bytes.Buffer{}}

	_ = corewriter.Register(&decoderTestFactory{sink: decoderSink, decoderUsed: &decoderUsedFlag, registeredAs: "c14-decoder"})
	_ = corewriter.Register(&plainTestFactory{sink: plainSink, registeredAs: "c14-plain"})
)

func TestFromConfig(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		blob string
	}
	tests := []tc{
		{"single decoder writer at info", `{"level":"info","writers":[{"name":"c14-decoder","config":{"k":"v"}}]}`},
		{"two writers fan out", `{"level":"debug","writers":[{"name":"c14-decoder","config":{}},{"name":"c14-plain","config":{}}]}`},
	}
	runCase := func(t *testing.T, blob string) {
		t.Helper()
		lg, err := logger.FromConfig(logger.Format("json"), []byte(blob))
		//: a well-formed topology over registered writers must build a Logger.
		if err != nil || lg == nil {
			t.Fatalf("FromConfig=(%v,%v) want (logger,nil)", lg, err)
		}
		//: the built logger must accept an emit without panicking.
		lg.Log(t.Context(), logger.LevelError, "hello")
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.blob)
		})
	}
}

func TestFromConfigDecoderBranch(t *testing.T) {
	//: NO t.Parallel here — this test mutates the package-global decoderUsedFlag,
	//: so it must run serially (KTN-TEST-NOPARALLEL); subtests stay serial too.
	type tc struct {
		name        string
		writerName  string
		wantDecoder bool
	}
	tests := []tc{
		{"Decoder factory uses Decode", "c14-decoder", true},
		{"plain factory uses default mapping", "c14-plain", false},
	}
	runCase := func(t *testing.T, writerName string, wantDecoder bool) {
		t.Helper()
		decoderUsedFlag = false
		blob := `{"level":"info","writers":[{"name":"` + writerName + `","config":{"opt":"x"}}]}`
		_, err := logger.FromConfig(logger.Format("json"), []byte(blob))
		//: both branches must build successfully.
		if err != nil {
			t.Fatalf("FromConfig: unexpected error %v", err)
		}
		//: the decoder flag distinguishes the two resolution branches.
		if decoderUsedFlag != wantDecoder {
			t.Errorf("decoderUsed=%v want %v", decoderUsedFlag, wantDecoder)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c.writerName, c.wantDecoder)
		})
	}
}

func TestFromConfigErrors(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		format string
		blob   string
	}
	tests := []tc{
		{"unknown writer name", "json", `{"level":"info","writers":[{"name":"c14-nope","config":{}}]}`},
		{"malformed blob", "json", `{not json at all`},
		{"unregistered format", "no-such-format", `{"writers":[]}`},
		{"empty writers", "json", `{"level":"info","writers":[]}`},
	}
	runCase := func(t *testing.T, format, blob string) {
		t.Helper()
		_, err := logger.FromConfig(logger.Format(format), []byte(blob))
		//: every failure arm must surface the redacted TopologyInvalid code.
		if !errs.HasCode(err, topologyInvalidCode) {
			t.Errorf("FromConfig err=%v want TopologyInvalid (1.1.0.4)", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.format, c.blob)
		})
	}
}

func TestFromConfigRedacts(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		secret string
	}
	tests := []tc{
		{"credential value never appears in error text", "SUPER-SECRET-AKIA-VALUE"},
	}
	runCase := func(t *testing.T, secret string) {
		t.Helper()
		//: an unknown writer carrying a secret option must error WITHOUT echoing it.
		blob := `{"level":"info","writers":[{"name":"c14-nope","config":{"secret_key":"` + secret + `"}}]}`
		_, err := logger.FromConfig(logger.Format("json"), []byte(blob))
		//: the error path must be the redacted topology sentinel.
		if !errs.HasCode(err, topologyInvalidCode) {
			t.Fatalf("FromConfig err=%v want TopologyInvalid", err)
		}
		//: neither the rendered error nor the public message may contain the secret
		//: (Error() never renders Private/Fields by ADR 0005, so this covers the
		//: full consumer-visible surface).
		if contains(err.Error(), secret) || contains(errs.PublicOf(err), secret) {
			t.Errorf("error leaked the secret option value")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.secret)
		})
	}
}

// contains reports whether s holds sub, without importing strings into the
// table helpers (keeps the assertion explicit at the call site).
func contains(s, sub string) bool {
	//: empty needle is trivially present; mirror strings.Contains semantics.
	return bytes.Contains([]byte(s), []byte(sub))
}
