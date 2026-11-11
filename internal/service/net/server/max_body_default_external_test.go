// Package server_test — the default bound on a request body an HTTP group
// serves through net/http (#845).
package server_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/net/server"
)

// bodyAnswer is what the handler under test did with a body.
const bodyAnswer string = "read"

// startReadAllServer serves a group, built with opts, whose handler reads the
// whole body and answers how many bytes it got, or 413 when the body's own
// bound ended the read, as a handler that maps *http.MaxBytesError does.
func startReadAllServer(t *testing.T, opts ...server.GroupOption) *server.Server {
	t.Helper()
	srv := server.New()
	opts = append([]server.GroupOption{server.Listen("tcp", "127.0.0.1:0"), server.Plaintext()}, opts...)
	srv.Group("api", opts...).HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, "%s %d", bodyAnswer, len(body))
	}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	return srv
}

// postStatus sends a POST whose head declares length bytes and whose body is
// the first sent of them, then reads the answer. A server that refuses on the head answers while most of the body is
// still unsent; one that reads on takes sent bytes and waits for the rest, so
// the caller sends all of it or the read ends at the connection's deadline.
func postStatus(t *testing.T, addr string, length, sent int) (status int, answer string) {
	t.Helper()
	conn, err := stdnet.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, conn) })
	if deadlineErr := conn.SetDeadline(time.Now().Add(boundsDeadline)); deadlineErr != nil {
		t.Fatalf("deadline: %v", deadlineErr)
	}
	if _, writeErr := fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n", length); writeErr != nil {
		t.Fatalf("write the head: %v", writeErr)
	}
	//: the body goes on its own goroutine: a server that has already refused
	//: stops reading, and the write then fails or blocks.
	go func() {
		chunk := []byte(strings.Repeat("a", 32<<10))
		for left := sent; left > 0; left -= len(chunk) {
			if _, writeErr := conn.Write(chunk[:min(left, len(chunk))]); writeErr != nil {
				return
			}
		}
	}()
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read the response: %v", err)
	}
	defer closeOrFail(t, response.Body)
	//: the answers are small and carry a length: the read ends with the body.
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	return response.StatusCode, string(body)
}

// TestMaxBodyBytes_defaultBoundRefusesA2MiBBody pins the default: a group that
// gave no MaxBodyBytes refuses a declared body of 2 MiB with 413 on the head,
// before the handler's ReadAll can return, and keeps serving a body within the
// 1 MiB bound.
func TestMaxBodyBytes_defaultBoundRefusesA2MiBBody(t *testing.T) {
	t.Parallel()
	addr := startReadAllServer(t).State().Listeners[0].Address
	//: only a kilobyte of the 2 MiB is ever sent: the refusal comes on the head.
	if status, _ := postStatus(t, addr, 2<<20, 1<<10); status != http.StatusRequestEntityTooLarge {
		t.Errorf("a 2 MiB POST under the default bound answered %d, want 413", status)
	}
	if status, answer := postStatus(t, addr, 512<<10, 512<<10); status != http.StatusOK || !strings.Contains(answer, bodyAnswer+" 524288") {
		t.Errorf("a 512 KiB POST under the default bound answered %d %q, want 200 and the whole body", status, answer)
	}
}
