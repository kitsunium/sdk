package json_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/json"
)

type sampleEvent struct {
	ID       string `json:"id"`
	Severity int    `json:"severity"`
}

func TestNew(t *testing.T) {
	t.Parallel()
	c := json.New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Name() != "json" {
		t.Errorf("Name = %q", c.Name())
	}
}

func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   sampleEvent
		want string
	}
	tests := []tc{
		{"round-trip", sampleEvent{ID: "x", Severity: 7}, `{"id":"x","severity":7}`},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		data, err := json.New().Marshal(c.in)
		if err != nil {
			t.Fatalf("%s: Marshal err = %v", c.name, err)
		}
		if string(data) != c.want {
			t.Errorf("%s: got %q want %q", c.name, data, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestUnmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    string
		wantErr bool
	}
	tests := []tc{
		{"valid", `{"id":"y","severity":4}`, false},
		{"malformed wraps UNMARSHAL_FAILED", `{"id":`, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got sampleEvent
		err := json.New().Unmarshal([]byte(c.data), &got)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
		}
		if c.wantErr && !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Errorf("%s: missing UNMARSHAL_FAILED reason: %v", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestStreaming(t *testing.T) {
	t.Parallel()
	c, ok := json.New().(codec.StreamingCodec)
	if !ok {
		t.Fatal("json codec does not implement StreamingCodec")
	}
	var buf bytes.Buffer
	enc := c.NewEncoder(&buf)
	if err := enc.Encode(sampleEvent{ID: "a", Severity: 1}); err != nil {
		t.Fatalf("Encode err = %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}
	dec := c.NewDecoder(&buf)
	var got sampleEvent
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("Decode err = %v", err)
	}
	if got.ID != "a" || got.Severity != 1 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	if _, ok := codec.Lookup(codec.Format("json")); !ok {
		t.Error("json codec not registered")
	}
	if _, ok := codec.LookupMIME("application/json"); !ok {
		t.Error("application/json not resolved")
	}
	if _, ok := codec.LookupExt(".json"); !ok {
		t.Error(".json extension not resolved")
	}
}
