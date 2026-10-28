// Package server — the http1 chain on a loop group, every backend present:
// N connections, each with its own token, served concurrently on two lanes,
// read buffers marked as the batch releases them (portable), under -race.
package server

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// conformanceVerifier maps "tok-i" to "user-i".
type conformanceVerifier struct{}

func (conformanceVerifier) VerifyBearer(token []byte, auth *corenet.HTTP1AuthValue) bool {
	i, ok := tokenIndex(token)
	if !ok {
		return false
	}
	auth.SetClaims(uint64(i))
	return auth.SetSubject([]byte("user-" + strconv.Itoa(i)))
}

// tokenIndex reads i out of "tok-i".
func tokenIndex(token []byte) (int, bool) {
	if len(token) < 5 || string(token[:4]) != "tok-" {
		return 0, false
	}
	i, err := strconv.Atoi(string(token[4:]))
	return i, err == nil
}

// authLog keeps, for every request served, the token as it was sent and the
// Auth value the chain left, by value: checked once every buffer has been
// reused many times over.
type authLog struct {
	mu   sync.Mutex
	recs []authRec
}

type authRec struct {
	token string
	auth  corenet.HTTP1AuthValue
	lane  int
}

// TestLoopChain_tokensStayTheirs is P2's conformance: N connections × N
// tokens through Routes and Bearer behind http1.HandleLoop, on every loop
// backend this platform has, two lanes. Each answer names its own
// connection's subject, an unauthenticated request in between is refused
// 401 on the same lane, and every Auth value the handler kept still names the
// subject of the token it came with after the buffers that carried the
// tokens were released, marked and reused — the subject is copied, never
// aliased.
func TestLoopChain_tokensStayTheirs(t *testing.T) {
	const conns, rounds = 24, 40
	markRecycled.Store(true)
	t.Cleanup(func() { markRecycled.Store(false) })
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			log := &authLog{}
			routes, err := http1.Routes(http1.RouteValue{ID: 1, Path: "/whoami"}, http1.RouteValue{ID: 2, Path: "/health"})
			if err != nil {
				t.Fatal(err)
			}
			bearer, err := http1.Bearer(conformanceVerifier{}, 2)
			if err != nil {
				t.Fatal(err)
			}
			app := corenet.HTTP1HandlerFunc(func(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
				if req.Route != 1 {
					res.Status = http.StatusNoContent
					return
				}
				tok := string(req.Authorization[len("Bearer "):])
				log.mu.Lock()
				log.recs = append(log.recs, authRec{token: tok, auth: req.Auth, lane: req.Lane})
				log.mu.Unlock()
				res.Body = append([]byte(nil), req.Auth.Subject()...)
			})
			h := http1.HandleLoop(corenet.Chain[corenet.HTTP1Handler](app, routes, bearer), http1.Parser{})
			_, addr := startLoopServer(t, b, h, Loop(corenet.LoopValue{Backend: b, Loops: 2}), Plaintext())
			var wg sync.WaitGroup
			errc := make(chan error, conns)
			for i := range conns {
				wg.Go(func() { errc <- tokenRounds(t, addr, i, rounds) })
			}
			wg.Wait()
			close(errc)
			for err := range errc {
				if err != nil {
					t.Fatal(err)
				}
			}
			log.mu.Lock()
			defer log.mu.Unlock()
			if len(log.recs) != conns*rounds {
				t.Fatalf("%d requests served, want %d", len(log.recs), conns*rounds)
			}
			lanes := map[int]bool{}
			for _, r := range log.recs {
				i, _ := tokenIndex([]byte(r.token))
				if want := "user-" + strconv.Itoa(i); string(r.auth.Subject()) != want || r.auth.Claims() != uint64(i) {
					t.Fatalf("token %s kept subject %q claims %d, want %q", r.token, r.auth.Subject(), r.auth.Claims(), want)
				}
				if r.lane < 0 || r.lane >= 2 {
					t.Fatalf("lane %d outside [0, 2)", r.lane)
				}
				lanes[r.lane] = true
			}
			//: the portable backend deals connections to its lanes in turn; a
			//: native one leaves it to the kernel, which may wake one loop only.
			if b == corenet.LoopBackendPortable && len(lanes) != 2 {
				t.Fatalf("served on lanes %v, want both", lanes)
			}
		})
	}
}

// tokenRounds runs rounds requests on one connection with token i, each
// followed by an unauthenticated one that must be refused, and checks every
// answer.
func tokenRounds(t *testing.T, addr string, i, rounds int) error {
	c := loopDial(t, addr)
	r := bufio.NewReader(c)
	want := "user-" + strconv.Itoa(i)
	authed := []byte("GET /whoami HTTP/1.1\r\nHost: h\r\nAuthorization: Bearer tok-" + strconv.Itoa(i) + "\r\n\r\n")
	anon := []byte("GET /whoami HTTP/1.1\r\nHost: h\r\n\r\n")
	for k := range rounds {
		if _, err := c.Write(authed); err != nil {
			return err
		}
		res, err := http.ReadResponse(r, nil)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK || string(body) != want {
			return fmt.Errorf("conn %d round %d: %d %q, want 200 %q", i, k, res.StatusCode, body, want)
		}
		if _, err := c.Write(anon); err != nil {
			return err
		}
		res, err = http.ReadResponse(r, nil)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, res.Body)
		if res.StatusCode != http.StatusUnauthorized {
			return fmt.Errorf("conn %d round %d: unauthenticated answered %d", i, k, res.StatusCode)
		}
	}
	return nil
}
