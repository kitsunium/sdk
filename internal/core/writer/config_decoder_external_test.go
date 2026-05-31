package writer_test

import (
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	corewriter "github.com/kitsunium/sdk/internal/core/writer"
)

// decoderFactory is a test Factory that also implements Decoder, so the
// type-assertion-extension path can be exercised without a real writer.
type decoderFactory struct {
	// decoded captures the last map handed to Decode for assertions.
	decoded map[string]any
}

// compile-time proof that the fakes satisfy the interfaces they stand in for.
var (
	_ corewriter.Factory = (*decoderFactory)(nil)
	_ corewriter.Decoder = (*decoderFactory)(nil)
	_ corewriter.Factory = (*plainFactory)(nil)
)

// Name reports a fixed key; never registered, so it cannot collide.
func (f *decoderFactory) Name() corewriter.Name {
	//: a constant key keeps the fake self-contained.
	return corewriter.Name("decoder-fake")
}

// Open is unused by these tests; it returns a nil sink and no error.
func (f *decoderFactory) Open(_ corewriter.Config) (corelogger.Sink, error) {
	//: the Decoder tests never open a sink.
	return nil, nil
}

// Decode records raw and echoes it back as the typed Config.
func (f *decoderFactory) Decode(raw map[string]any) (corewriter.Config, error) {
	//: stash the map so the test can assert the extension was invoked.
	f.decoded = raw
	//: echo the map back as the opaque Config.
	return raw, nil
}

// plainFactory is a test Factory that does NOT implement Decoder, so the
// negative branch of the type assertion can be exercised.
type plainFactory struct{}

// Name reports a fixed key distinct from decoderFactory.
func (*plainFactory) Name() corewriter.Name {
	//: a constant key keeps the fake self-contained.
	return corewriter.Name("plain-fake")
}

// Open is unused by these tests; it returns a nil sink and no error.
func (*plainFactory) Open(_ corewriter.Config) (corelogger.Sink, error) {
	//: the assertion tests never open a sink.
	return nil, nil
}

func TestDecoderTypeAssertion(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		factory     corewriter.Factory
		wantDecoder bool
	}
	tests := []tc{
		{"factory implementing Decoder asserts true", &decoderFactory{}, true},
		{"factory without Decode asserts false", &plainFactory{}, false},
	}
	runCase := func(t *testing.T, factory corewriter.Factory, wantDecoder bool) {
		t.Helper()
		_, ok := factory.(corewriter.Decoder)
		//: the assertion outcome must match whether the fake implements it.
		if ok != wantDecoder {
			t.Errorf("Decoder assertion = %v, want %v", ok, wantDecoder)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.factory, c.wantDecoder)
		})
	}
}

func TestDecoderEchoesMap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     map[string]any
		wantLen int
	}
	tests := []tc{
		{"empty map round-trips", map[string]any{}, 0},
		{"populated map round-trips", map[string]any{"path": "/tmp/x", "level": "warn"}, 2},
	}
	runCase := func(t *testing.T, raw map[string]any, wantLen int) {
		t.Helper()
		//: drive the fake through the Decoder interface so the optional
		//: extension's contract (echo the map back as Config) is what is tested.
		var decoder corewriter.Decoder = &decoderFactory{}
		cfg, err := decoder.Decode(raw)
		//: the decoder must succeed and echo the map back as the Config.
		if err != nil {
			t.Fatalf("Decode: unexpected error %v", err)
		}
		got, ok := cfg.(map[string]any)
		//: the returned Config must be the same map the decoder received.
		if !ok || len(got) != wantLen {
			t.Errorf("Decode cfg=%v want map len %d", cfg, wantLen)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.raw, c.wantLen)
		})
	}
}
