package cloudwatch

import (
	"context"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
)

// fakeProviderWithToken returns fixed credentials carrying a non-empty session
// token, so the adapter's SessionToken mapping has a value to round-trip.
type fakeProviderWithToken struct{ token string }

func (f fakeProviderWithToken) Credentials(_ context.Context) (writer.CredentialValue, error) {
	//: temporary STS-style material — access key + secret + session token.
	return writer.NewCredentialValue("AKIATEST", "secret", f.token), nil
}

func Test_credAdapter_Retrieve(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		prov      writer.CredentialProvider
		wantErr   bool
		wantAK    string
		wantToken string
	}
	tests := []tc{
		{"maps credential value onto the SDK struct", fakeProvider{}, false, "AKIATEST", ""},
		{"session token round-trips", fakeProviderWithToken{token: "FQoGZ-session"}, false, "AKIATEST", "FQoGZ-session"},
		{"propagates a provider error", fakeProvider{err: errors.New("no creds")}, true, "", ""},
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
		//: happy arm — every credential field must round-trip into aws.Credentials,
		//: and Source must be the adapter's fixed provenance label.
		if err != nil || got.AccessKeyID != c.wantAK || got.SecretAccessKey != "secret" ||
			got.SessionToken != c.wantToken || got.Source != "kitsunium/awswriters" {
			t.Errorf("%s: err=%v got=%+v want AK=%q secret=%q token=%q source=%q",
				c.name, err, got, c.wantAK, "secret", c.wantToken, "kitsunium/awswriters")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
