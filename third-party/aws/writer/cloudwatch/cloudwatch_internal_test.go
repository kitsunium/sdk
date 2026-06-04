package cloudwatch

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_cwFactory_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want writer.Name
	}
	tests := []tc{{"reports the canonical key", "cloudwatch"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the factory must report the key it registered under.
		if got := (&cwFactory{}).Name(); got != c.want {
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

func Test_cloudwatchRegistered(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"importing the package registers the cloudwatch writer"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: the package-level Writer var must have self-registered.
		if !writer.Name("cloudwatch").Known() {
			t.Errorf("cloudwatch writer not registered")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwFactory_Open(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		cfg      writer.Config
		wantErr  bool
		wantCode errs.Code
	}
	tests := []tc{
		{"wrong config type rejected", writer.FileConfig{Path: "/x"}, true, writer.CodeWriterConfigInvalid},
		{"missing group rejected", writer.CloudWatchConfig{Stream: "s", Region: "eu-west-3", Credentials: fakeProvider{}}, true, writer.CodeWriterConfigInvalid},
		{"missing stream rejected", writer.CloudWatchConfig{Group: "g", Region: "eu-west-3", Credentials: fakeProvider{}}, true, writer.CodeWriterConfigInvalid},
		{"missing region rejected", writer.CloudWatchConfig{Group: "g", Stream: "s", Credentials: fakeProvider{}}, true, writer.CodeWriterConfigInvalid},
		{"nil credentials surface ClientInitFailed", writer.CloudWatchConfig{Group: "g", Stream: "s", Region: "eu-west-3"}, true, CodeCWClientInitFailed},
		{"valid config builds a sink", writer.CloudWatchConfig{Group: "g", Stream: "s", Region: "eu-west-3", Credentials: fakeProvider{}}, false, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := (&cwFactory{}).Open(c.cfg)
		//: failure arms — error with the expected code, nil sink.
		if c.wantErr {
			if err == nil || sink != nil {
				t.Fatalf("%s: err=%v sink=%v want error+nil", c.name, err, sink)
			}
			if !errs.HasCode(err, c.wantCode) {
				t.Errorf("%s: HasCode(%v)=false, err=%v", c.name, c.wantCode, err)
			}
			//: the ClientInitFailed sentinel is Defined without WithExitCode, so its
			//: wrap decays to the sysexits default EX_SOFTWARE (70). Pinning it here
			//: means a future errs.WithExitCode on that sentinel would break this
			//: assertion deliberately, forcing the change to be reviewed.
			const wantClientInitExit int = 70
			if c.wantCode == CodeCWClientInitFailed {
				if got := errs.ExitCodeOf(err); got != wantClientInitExit {
					t.Errorf("%s: ExitCodeOf=%d want %d", c.name, got, wantClientInitExit)
				}
			}
			return
		}
		//: happy arm — a usable sink with no error (no network at build time).
		if err != nil || sink == nil {
			t.Fatalf("%s: err=%v sink=%v want nil+sink", c.name, err, sink)
		}
		//: surface a close failure rather than discarding it.
		if cerr := sink.Close(); cerr != nil {
			t.Errorf("%s: close: %v", c.name, cerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
