package kit

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"testing/fstest"
)

// The product's binary says its configuration: every setting, its value —
// never a secret's — and where it comes from.
func TestTheConfigCommand(t *testing.T) {
	svc := NewService("notify", "Mail and its links.")
	svc.Setting("base-url", "http://localhost:4000")
	svc.Setting("page-size", 50)
	svc.Setting("sender", "", Required())
	svc.Secret("api-token")
	t.Setenv("NEWS_SENDER", "ops@example.com")
	t.Setenv("NEWS_API_TOKEN", "tok-123")
	t.Setenv("KIT_SECRETS", "memory")
	svc.Mailer("mail")
	t.Setenv("KIT_SMTP_URL", "smtp://ops:hunter2-pw@relay.example.com:587?tls=starttls")
	app := NewApp("news", svc).With(InMemory(), Logs(io.Discard),
		ConfigFiles(fstest.MapFS{"config/config.yaml": {Data: []byte("page-size: 20\n")}}))
	var out, errOut bytes.Buffer
	if code := app.configCommand(t.Context(), nil, &out, &errOut); code != 0 {
		t.Fatalf("config: %d\n%s", code, errOut.String())
	}
	text := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{
		"NEWS_SENDER ops@example.com env notify · sender",
		"NEWS_PAGE_SIZE 20 file config/config.yaml notify · page-size",
		"NEWS_BASE_URL http://localhost:4000 default notify · base-url",
		"KIT_ENV",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "tok-123") || strings.Contains(out.String(), "hunter2-pw") {
		t.Errorf("the configuration shows a secret:\n%s", out.String())
	}
	if !strings.Contains(text, "KIT_SMTP_URL (secret) env kit") {
		t.Errorf("KIT_SMTP_URL is not said to be a secret set in the environment:\n%s", out.String())
	}
	for line := range strings.SplitSeq(out.String(), "\n") {
		if f := strings.Fields(line); len(f) >= 3 && f[0] == "KIT_STUDIO" && strings.Contains(line, "kit.Studio") {
			t.Errorf("KIT_STUDIO, from the %s, is said to come from its option: %q", f[2], line)
		}
	}

	t.Setenv("NEWS_PAGE_SIZE", "many")
	out.Reset()
	errOut.Reset()
	if code := app.configCommand(t.Context(), nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "NEWS_PAGE_SIZE is not a whole number") {
		t.Errorf("a refused configuration: %d %q", code, errOut.String())
	}

	// A start refused for anything else — a secret nobody gives — is refused
	// by the command too.
	t.Setenv("NEWS_PAGE_SIZE", "")
	t.Setenv("NEWS_API_TOKEN", "")
	out.Reset()
	errOut.Reset()
	if code := app.configCommand(t.Context(), nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "secret notify/secret/api-token is not set") {
		t.Errorf("an unset secret: %d %q", code, errOut.String())
	}
}
