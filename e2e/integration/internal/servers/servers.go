//go:build integration

// Package servers starts the servers the vendor broker and cache suites run
// against — Valkey and NATS with JetStream — with testcontainers, or hands
// back one the environment names (VALKEY_ADDR, NATS_URL), so a lane that
// runs its servers as services, or a machine with one installed, needs no
// Docker. A server that cannot start skips the calling test.
package servers

import (
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Valkey returns the address of a Valkey server: VALKEY_ADDR, or a
// valkey/valkey:8 container removed when the test ends.
func Valkey(t *testing.T) string {
	t.Helper()
	if addr := os.Getenv("VALKEY_ADDR"); addr != "" {
		return addr
	}
	ctr, err := testcontainers.Run(t.Context(), "valkey/valkey:8",
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("6379/tcp")),
	)
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	addr, err := ctr.PortEndpoint(t.Context(), "6379/tcp", "")
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	return addr
}

// NATS returns the URL of a NATS server with JetStream: NATS_URL, or a
// nats:2.11 container started with -js, removed when the test ends.
func NATS(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("NATS_URL"); url != "" {
		return url
	}
	ctr, err := testcontainers.Run(t.Context(), "nats:2.11",
		testcontainers.WithCmd("-js"),
		testcontainers.WithExposedPorts("4222/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("4222/tcp")),
	)
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	addr, err := ctr.PortEndpoint(t.Context(), "4222/tcp", "nats")
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	return addr
}
