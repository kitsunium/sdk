//go:build !linux

package service

import (
	"testing"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// TestStoreRefusedOffLinux: no notify socket but Linux's carries descriptors,
// so FileDescriptorStoreMax= is DIRECTIVE_UNSUPPORTED everywhere else.
func TestStoreRefusedOffLinux(t *testing.T) {
	cfg := Config{Name: "u", Notify: stubNotify{}, FileDescriptorStoreMax: 1, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}}
	if _, err := resolve(&cfg); !hasCode(err, coresvc.CodeDirectiveUnsupported) || field(err, "directive") != "FileDescriptorStoreMax" {
		t.Fatalf("FileDescriptorStoreMax=1: %v, want DIRECTIVE_UNSUPPORTED", err)
	}
}
