package mail_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	svcmail "github.com/kitsunium/sdk/internal/service/mail"
)

// renderPassword is the password every rendering below must not print.
const renderPassword string = "sm7p-p4ssw0rd-9f1e"

// TestSMTPConfigNeverRendersItsPassword pins every rendering a configuration
// reaches for by accident: fmt with every verb, directly and nested, String,
// GoString and JSON. Before Format existed, %+v printed the password.
func TestSMTPConfigNeverRendersItsPassword(t *testing.T) {
	t.Parallel()
	cfg := svcmail.SMTPConfig{
		Host: "relay.example", Port: 587, TLS: svcmail.TLSStartTLS,
		Username: "postmaster", Password: renderPassword, DialTimeout: time.Second,
	}
	type wrapper struct {
		SMTP svcmail.SMTPConfig
	}
	marshal := func(v any) string {
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		return string(encoded)
	}
	type tc struct {
		name, rendered string
	}
	tests := []tc{
		{"%v", fmt.Sprintf("%v", cfg)},
		{"%+v", fmt.Sprintf("%+v", cfg)},
		{"%#v", fmt.Sprintf("%#v", cfg)},
		{"%s", fmt.Sprintf("%s", cfg)},
		{"%q", fmt.Sprintf("%q", cfg)},
		{"%x", fmt.Sprintf("%x", cfg)},
		{"a pointer", fmt.Sprintf("%+v", &cfg)},
		{"nested", fmt.Sprintf("%+v", wrapper{SMTP: cfg})},
		{"String", cfg.String()},
		{"GoString", cfg.GoString()},
		{"json", marshal(cfg)},
		{"json, nested", marshal(wrapper{SMTP: cfg})},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if strings.Contains(c.rendered, renderPassword) || strings.Contains(c.rendered, fmt.Sprintf("%x", renderPassword)) {
			t.Fatalf("%s printed the password: %s", c.name, c.rendered)
		}
		//: the rest of the configuration is still there to debug with.
		if !strings.Contains(c.rendered, "relay.example") && !strings.Contains(c.rendered, fmt.Sprintf("%x", "relay.example")) {
			t.Errorf("%s lost the host: %s", c.name, c.rendered)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	if got := fmt.Sprintf("%+v", cfg); !strings.Contains(got, "Password:<redacted>") || !strings.Contains(got, "TLS:starttls") {
		t.Errorf("%%+v = %s, want the placeholder where the password was", got)
	}
	if got := fmt.Sprintf("%#v", cfg); !strings.HasPrefix(got, "mail.SMTPConfig{") {
		t.Errorf("%%#v = %s, want it named as the type it is", got)
	}
	//: no password is not a secret, and is rendered as the empty string.
	if got := fmt.Sprintf("%+v", svcmail.SMTPConfig{Host: "h"}); !strings.Contains(got, "Password: ") {
		t.Errorf("an unset password rendered as %s", got)
	}
}
