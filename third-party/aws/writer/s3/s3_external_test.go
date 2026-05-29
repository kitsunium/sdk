package s3_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	_ "github.com/kitsunium/sdk/third-party/aws/writer/s3"
)

// extProvider is a public-side CredentialProvider for the black-box test.
type extProvider struct{}

// : compile-time proof the provider satisfies the credential port.
var _ writer.CredentialProvider = (*extProvider)(nil)

func (extProvider) Credentials(_ context.Context) (writer.CredentialValue, error) {
	//: fixed material; the external test never performs a real upload.
	return writer.NewCredentialValue("AKIAEXT", "secret", ""), nil
}

func TestOpenViaRegistry(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		cfg      writer.Config
		wantErr  bool
		wantSink bool
	}
	tests := []tc{
		{"valid config resolves and builds a sink", writer.S3Config{Bucket: "b", Region: "eu-west-3", Credentials: extProvider{}}, false, true},
		{"wrong config type is rejected", writer.FileConfig{Path: "/x"}, true, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := writer.Open("s3", c.cfg)
		//: failure arm — error + nil sink.
		if c.wantErr {
			if err == nil || sink != nil {
				t.Errorf("%s: err=%v sink=%v want error+nil", c.name, err, sink)
			}
			return
		}
		//: happy arm — a usable sink built offline; release it afterward.
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
