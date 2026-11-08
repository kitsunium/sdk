package dbus_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/dbus"
	"github.com/kitsunium/sdk/pkg/v1/proc/service"
)

// A Conn is what a Type=dbus unit's Config.BusWatcher takes, as it is.
var _ service.BusWatcher = (*dbus.Conn)(nil)

// TestNewConnRefusesWhatItCannotDial: through the facade, an address with no
// unix socket is ADDRESS_INVALID on Linux, and NewConn is refused elsewhere —
// nothing is dialled either way.
func TestNewConnRefusesWhatItCannotDial(t *testing.T) {
	_, err := dbus.NewConn(context.Background(), "tcp:host=localhost,port=1")
	if runtime.GOOS == "linux" && !errs.HasCode(err, dbus.CodeAddressInvalid) {
		t.Fatalf("a TCP address: %v, want ADDRESS_INVALID", err)
	}
	if err == nil {
		t.Fatal("a TCP address was dialled")
	}
}
