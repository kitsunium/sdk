package s3

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_s3Factory_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want writer.Name
	}
	tests := []tc{{"reports the canonical key", "s3"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the factory must report the key it registered under.
		if got := (&s3Factory{}).Name(); got != c.want {
			t.Errorf("%s: Name()=%q want %q", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_s3Registered(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"importing the package registers the s3 writer"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: the package-level Writer var must have self-registered.
		if !writer.Name("s3").Known() {
			t.Errorf("s3 writer not registered")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_s3Factory_Open(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		cfg      writer.Config
		wantErr  bool
		wantCode errs.Code
		wantSink bool
	}
	tests := []tc{
		{"wrong config type rejected", writer.FileConfig{Path: "/x"}, true, writer.CodeWriterConfigInvalid, false},
		{"empty bucket rejected", writer.S3Config{Region: "eu-west-3", Credentials: fakeProvider{}}, true, writer.CodeWriterConfigInvalid, false},
		{"empty region rejected", writer.S3Config{Bucket: "b", Credentials: fakeProvider{}}, true, writer.CodeWriterConfigInvalid, false},
		{"nil credentials surface ClientInitFailed", writer.S3Config{Bucket: "b", Region: "eu-west-3"}, true, CodeS3ClientInitFailed, false},
		{"valid config builds a sink", writer.S3Config{Bucket: "b", Region: "eu-west-3", Credentials: fakeProvider{}}, false, 0, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := (&s3Factory{}).Open(c.cfg)
		//: failure arms — error with the expected dotted-quad code, nil sink.
		if c.wantErr {
			if err == nil || sink != nil {
				t.Fatalf("%s: err=%v sink=%v want error+nil", c.name, err, sink)
			}
			if !errs.HasCode(err, c.wantCode) {
				t.Errorf("%s: HasCode(%v)=false, err=%v", c.name, c.wantCode, err)
			}
			return
		}
		//: happy arm — a usable sink with no error (no network at build time).
		if err != nil || sink == nil {
			t.Errorf("%s: err=%v sink=%v want nil+sink", c.name, err, sink)
		}
		//: release the async drainer + ticker the happy path started.
		if sink != nil {
			//: surface a close failure rather than discarding it.
			if cerr := sink.Close(); cerr != nil {
				t.Errorf("%s: close: %v", c.name, cerr)
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
