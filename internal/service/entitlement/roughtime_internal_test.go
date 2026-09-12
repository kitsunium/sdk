package entitlement

import (
	"crypto/ed25519"
	"errors"
	"net"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// Test_buildRoughtimeRequest pins the padding rule, which is the single most
// likely reason a client sees perfect silence.
//
// A request under the protocol's floor is dropped without a reply — it is an
// anti-amplification rule, not a preference — and the floor applies to the
// FRAMED packet, not to the message inside it. Getting that wrong is
// indistinguishable from a filtered port, which is exactly how long such a bug
// survives.
func Test_buildRoughtimeRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
	}{
		{name: "the framed packet clears the protocol floor", reason: "a short request earns silence, which looks exactly like a filtered port"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			nonce := make([]byte, roughtimeNonceSize)
			packet := buildRoughtimeRequest(nonce)

			if len(packet) < roughtimeMinRequest {
				t.Errorf("buildRoughtimeRequest() = %d bytes, want at least %d (%s)", len(packet), roughtimeMinRequest, tt.reason)
			}
			if string(packet[:len(roughtimeFrameMagic)]) != roughtimeFrameMagic {
				t.Errorf("buildRoughtimeRequest() is not framed (%s)", tt.reason)
			}

			//: The frame must describe exactly what follows it, or a server
			//: reads past the message or stops short of it.
			message, err := unframeRoughtime(packet)
			if err != nil {
				t.Fatalf("unframeRoughtime() error = %v, want nil (%s)", err, tt.reason)
			}
			fields, decodeErr := decodeRoughtimeMessage(message)
			if decodeErr != nil {
				t.Fatalf("decodeRoughtimeMessage() error = %v, want nil (%s)", decodeErr, tt.reason)
			}
			//: The nonce is the whole point of the request; losing it would
			//: make every answer a replay of somebody else's.
			if got := fields[roughtimeTag("NONC")]; len(got) != roughtimeNonceSize {
				t.Errorf("request nonce = %d bytes, want %d (%s)", len(got), roughtimeNonceSize, tt.reason)
			}
		})
	}
}

// Test_roughtimeExchange pins that an unreachable server is reported as
// UNVERIFIABLE rather than as anything the caller might act on.
//
// The distinction carries the whole fail-open policy: checkNetworkTime treats
// "no answer" as no signal, so a transport failure that arrived wearing any
// other sentinel would turn a firewall rule into a refused licence.
func Test_roughtimeExchange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// address is where the request is sent.
		address func(t *testing.T) string
		reason  string
	}{
		{
			name:    "a malformed address",
			address: func(*testing.T) string { return "not-an-address" },
			reason:  "a dial failure is not a decision about anything",
		},
		{
			//: A real socket nobody is listening on: the datagram leaves and
			//: no answer comes back, which is the ordinary filtered-port case.
			name: "a port nothing answers on",
			address: func(t *testing.T) string {
				t.Helper()
				conn, err := net.ListenPacket("udp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("reserving a port: %v", err)
				}
				address := conn.LocalAddr().String()
				//: Close it so the port is free and silent.
				if closeErr := conn.Close(); closeErr != nil {
					t.Fatalf("releasing the port: %v", closeErr)
				}
				return address
			},
			reason: "silence is what a filtered port and a dropped request look like alike",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := roughtimeExchange(tt.address(t), buildRoughtimeRequest(make([]byte, roughtimeNonceSize)))
			if !errors.Is(err, coreent.ErrCIUnverifiable) {
				t.Errorf("roughtimeExchange() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
			}
		})
	}
}

// Test_Service_checkNetworkTime pins the fail-open policy, which is the whole
// safety of this feature.
//
// No server configured, none reachable, none whose answer verifies: all three
// must return nil and let the verification carry on. An adversary drops UDP to
// a nonstandard port for free, so failing closed would buy nothing against them
// while refusing every legitimate user behind a firewall that does the same
// thing by policy.
func Test_Service_checkNetworkTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// servers is what the Service is told to consult.
		servers []RoughtimeServerValue
		reason  string
	}{
		{name: "no server configured is no opinion", servers: nil, reason: "committed source ships an empty list, which must do nothing at all"},
		{
			name:    "an unreachable server is no opinion",
			servers: []RoughtimeServerValue{{Name: "stub", Address: "127.0.0.1:1", PublicKey: make([]byte, ed25519.PublicKeySize)}},
			reason:  "a firewall must never become a refusal",
		},
		{
			name:    "a server with no usable key is no opinion",
			servers: []RoughtimeServerValue{{Name: "stub", Address: "127.0.0.1:1"}},
			reason:  "a misconfigured list degrades to silence, not to a lockout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := (&Service{}).WithTimeServers(tt.servers)
			if err := svc.checkNetworkTime(time.Now()); err != nil {
				t.Errorf("checkNetworkTime() error = %v, want nil (%s)", err, tt.reason)
			}
		})
	}
}

// Test_Service_checkClockAndTime pins that the LOCAL ratchet runs first.
//
// The order is not arbitrary: the ratchet costs a file read and answers without
// anybody's permission, while the network check costs a round trip and is
// silent on most locked-down networks. A machine the ratchet can already refuse
// must not wait on a datagram to be told so.
func Test_Service_checkClockAndTime(t *testing.T) {
	t.Parallel()

	mark := time.Now().Truncate(time.Second)

	tests := []struct {
		name string
		// offset is applied to the mark to produce the clock reading.
		offset  time.Duration
		wantErr bool
		reason  string
	}{
		{name: "a regressed clock is refused without waiting on the network", offset: -time.Hour, wantErr: true, reason: "the local answer is free and immediate"},
		{name: "a sane clock passes both", offset: time.Hour, reason: "with no server configured the network half is silent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if keyErr != nil {
				t.Fatalf("generating vendor key: %v", keyErr)
			}

			dir := t.TempDir()
			if err := writeCachedBundle(dir, internalBundle(t, vendorPriv, mark, mark.Add(time.Minute))); err != nil {
				t.Fatalf("planting bundle: %v", err)
			}

			//: An unroutable server, so a network round trip would visibly
			//: cost the test its deadline rather than pass unnoticed.
			svc := (&Service{vendor: vendorPub, cacheDir: dir}).
				WithTimeServers([]RoughtimeServerValue{{Name: "stub", Address: "192.0.2.1:2002", PublicKey: make([]byte, ed25519.PublicKeySize)}})

			err := svc.checkClockAndTime(mark.Add(tt.offset))
			if tt.wantErr {
				if !errors.Is(err, coreent.ErrClockRegressed) {
					t.Errorf("checkClockAndTime() error = %v, want coreent.ErrClockRegressed (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Errorf("checkClockAndTime() error = %v, want nil (%s)", err, tt.reason)
			}
		})
	}
}
