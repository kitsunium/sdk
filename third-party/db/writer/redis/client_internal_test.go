package redis

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// staticCreds is a CredentialProvider returning fixed AUTH material.
type staticCreds struct{}

func (staticCreds) Credentials(context.Context) (writer.CredentialValue, error) {
	//: fixed user/password drives the AUTH branch deterministically.
	return writer.NewCredentialValue("default", "s3cr3t", ""), nil
}

// failCreds is a CredentialProvider that always fails.
type failCreds struct{}

func (failCreds) Credentials(context.Context) (writer.CredentialValue, error) {
	//: a fixed failure drives the credential-error branch.
	return writer.CredentialValue{}, errs.Wrap(nil, errs.WrapParams{
		Code: CodeRedisClientInitFailed, Reason: "CLIENT_INIT_FAILED",
		Public: "no creds", Private: "test failCreds",
	})
}

func Test_newClient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     writer.RedisStreamConfig
		wantErr bool
	}{
		{"missing socket rejected", writer.RedisStreamConfig{Stream: "logs"}, true},
		{"missing stream rejected", writer.RedisStreamConfig{SocketPath: "/tmp/r.sock"}, true},
		{"credential failure rejected", writer.RedisStreamConfig{SocketPath: "/tmp/r.sock", Stream: "logs", Credentials: failCreds{}}, true},
		{"valid without creds", writer.RedisStreamConfig{SocketPath: "/tmp/r.sock", Stream: "logs"}, false},
		{"valid with creds", writer.RedisStreamConfig{SocketPath: "/tmp/r.sock", Stream: "logs", Credentials: staticCreds{}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			exec, closeFn, err := newClient(tc.cfg)
			//: error arm — client-init sentinel, nil closure + closeFn.
			if tc.wantErr {
				if exec != nil || closeFn != nil || !errs.HasCode(err, CodeRedisClientInitFailed) {
					t.Fatalf("%s: execNil=%v closeNil=%v err=%v want nil+client-init", tc.name, exec == nil, closeFn == nil, err)
				}
				return
			}
			//: happy arm — a deliver closure + a closeFn.
			if err != nil || exec == nil || closeFn == nil {
				t.Fatalf("%s: execNil=%v closeNil=%v err=%v want closure+closeFn", tc.name, exec == nil, closeFn == nil, err)
			}
			//: release the lazily-built client (no connection was made).
			if cerr := closeFn(); cerr != nil {
				t.Errorf("%s: closeFn: %v", tc.name, cerr)
			}
		})
	}
}

func Test_resolveCreds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cp       writer.CredentialProvider
		wantUser string
		wantErr  bool
	}{
		{"nil provider yields empty AUTH", nil, "", false},
		{"static provider yields login", staticCreds{}, "default", false},
		{"failing provider errors", failCreds{}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			user, _, err := resolveCreds(tc.cp)
			//: error arm — a provider failure surfaces.
			if (err != nil) != tc.wantErr {
				t.Fatalf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
			}
			//: happy arm — the username maps from AccessKeyID().
			if !tc.wantErr && user != tc.wantUser {
				t.Errorf("%s: user=%q want %q", tc.name, user, tc.wantUser)
			}
		})
	}
}

func Test_buildValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rec  corelogger.RecordEvent
		want string
	}{
		{"maps level + message", corelogger.RecordEvent{Message: "hello", Level: level.Warn}, "hello"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vals := buildValues(tc.rec)
			//: the field set must carry ts, level, and message.
			if len(vals) != 3 || vals["message"] != tc.want {
				t.Errorf("%s: values=%v want message=%q + ts + level", tc.name, vals, tc.want)
			}
		})
	}
}
