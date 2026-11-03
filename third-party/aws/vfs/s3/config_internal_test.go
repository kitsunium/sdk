package s3

import (
	"context"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/observe/logger/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// staticCreds is a CredentialProvider with fixed test credentials.
type staticCreds struct{}

func (staticCreds) Credentials(_ context.Context) (writer.CredentialValue, error) {
	return writer.NewCredentialValue("test", "test", ""), nil
}

func validConfig() Config {
	return Config{Bucket: "b", Region: "eu-west-3", Credentials: staticCreds{}, MaxKeys: 1000, Timeout: time.Second}
}

func TestNewRefusesByFieldWhatNoRequestCouldFix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		edit  func(c *Config)
		field string
	}{
		{func(c *Config) { c.Bucket = "" }, "Bucket"},
		{func(c *Config) { c.Region = "" }, "Region"},
		{func(c *Config) { c.Credentials = nil }, "Credentials"},
		{func(c *Config) { c.Prefix = "." }, "Prefix"},
		{func(c *Config) { c.Prefix = "/abs" }, "Prefix"},
		{func(c *Config) { c.Prefix = "a//b" }, "Prefix"},
		{func(c *Config) { c.Prefix = "a/" }, "Prefix"},
		{func(c *Config) { c.MaxKeys = 0 }, "MaxKeys"},
		{func(c *Config) { c.MaxKeys = 1001 }, "MaxKeys"},
		{func(c *Config) { c.Timeout = 0 }, "Timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			tc.edit(&cfg)
			filesystem, err := New(cfg)
			if filesystem != nil || !errs.HasCode(err, CodeS3FSMisconfigured) {
				t.Fatalf("New = (%v, %v), want S3FS_MISCONFIGURED", filesystem, err)
			}
			if fields := errs.FieldsOf(err); len(fields) == 0 || fields[0].StringValue() != tc.field {
				t.Fatalf("New = %v, want the field %s named", err, tc.field)
			}
		})
	}
}

func TestNewBuildsAFilesystemWithoutSendingARequest(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Prefix, cfg.Endpoint = "assets/site", "http://127.0.0.1:1"
	filesystem, err := New(cfg)
	if err != nil || filesystem == nil {
		t.Fatalf("New = (%v, %v), want a filesystem", filesystem, err)
	}
	built := filesystem.(*s3FS)
	if got := built.objectKey("a.txt"); got != "assets/site/a.txt" {
		t.Fatalf("objectKey = %q, want assets/site/a.txt", got)
	}
}

func TestTheCredentialAdapterHandsOverTheProvidersCredentials(t *testing.T) {
	t.Parallel()
	creds, err := credAdapter{provider: staticCreds{}}.Retrieve(t.Context())
	if err != nil || creds.AccessKeyID != "test" || creds.SecretAccessKey != "test" {
		t.Fatalf("Retrieve = (%v, %v), want the provider's keys", creds.AccessKeyID, err)
	}
}
