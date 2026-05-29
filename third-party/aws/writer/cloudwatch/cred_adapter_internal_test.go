package cloudwatch

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
)

func Test_credAdapter_Retrieve(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		prov    writer.CredentialProvider
		wantErr bool
		wantAK  string
	}
	tests := []tc{
		{"maps credential value onto the SDK struct", fakeProvider{}, false, "AKIATEST"},
		{"propagates a provider error", fakeProvider{err: errors.New("no creds")}, true, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := credAdapter{provider: c.prov}.Retrieve(t.Context())
		//: failure arm — the provider error must propagate.
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got nil", c.name)
			}
			return
		}
		//: happy arm — the access key must round-trip into aws.Credentials.
		if err != nil || got.AccessKeyID != c.wantAK {
			t.Errorf("%s: err=%v accessKeyID=%q want %q", c.name, err, got.AccessKeyID, c.wantAK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
