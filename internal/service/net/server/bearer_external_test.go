package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// tokenVerifier accepts one token and names its subject.
type tokenVerifier struct{ token string }

func (v tokenVerifier) VerifyBearer(token []byte, auth *corenet.HTTP1AuthValue) bool {
	if string(token) != v.token {
		return false
	}
	return auth.SetSubject([]byte("svc"))
}

func TestBearer_netHTTP(t *testing.T) {
	mw, err := server.Bearer(tokenVerifier{token: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	var subject string
	reached := 0
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached++
		auth, ok := server.AuthOf(r.Context())
		if !ok {
			t.Error("AuthOf found no auth behind Bearer")
		}
		subject = string(auth.Subject())
	}))
	for _, tc := range []struct {
		name, field string
		code        int
	}{
		{"no field", "", 401},
		{"other scheme", "Basic czNjcmV0", 401},
		{"scheme only", "Bearer ", 401},
		{"no space", "Bearers3cret", 401},
		{"wrong token", "Bearer nope", 401},
		{"right token", "Bearer s3cret", 200},
		{"scheme case and spaces", "bEaReR   s3cret", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := reached
			r := httptest.NewRequest("GET", "/", nil)
			if tc.field != "" {
				r.Header.Set("Authorization", tc.field)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("%q answered %d, want %d", tc.field, w.Code, tc.code)
			}
			if tc.code == 401 {
				if reached != before || w.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Fatalf("refusal reached the handler or lacks the challenge: %v", w.Header())
				}
				return
			}
			if reached != before+1 || subject != "svc" {
				t.Fatalf("reached %d, subject %q", reached-before, subject)
			}
		})
	}
}

func TestBearer_refusesNilVerifier(t *testing.T) {
	if _, err := server.Bearer(nil); !errs.HasCode(err, corenet.CodeHTTP1Misconfigured) {
		t.Fatalf("nil verifier: %v", err)
	}
}

func TestAuthOf_withoutBearer(t *testing.T) {
	if _, ok := server.AuthOf(httptest.NewRequest("GET", "/", nil).Context()); ok {
		t.Fatal("AuthOf found an auth no middleware set")
	}
}
