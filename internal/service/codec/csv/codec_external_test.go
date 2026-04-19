package csv_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/csv"
)

func TestNew(t *testing.T) {
	t.Parallel()
	c := csv.New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Name() != "csv" {
		t.Errorf("Name = %q", c.Name())
	}
}

func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr bool
		wantSub string
	}
	tests := []tc{
		{"matrix", [][]string{{"a", "b"}, {"c", "d"}}, false, "a,b"},
		{"pointer to matrix", &[][]string{{"x"}}, false, "x"},
		{"wrong type", "nope", true, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		data, err := csv.New().Marshal(c.in)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
		}
		if c.wantErr {
			if !errs.HasReason(err, "VALUE_INVALID") {
				t.Errorf("%s: expected VALUE_INVALID, got %v", c.name, err)
			}
			return
		}
		if len(data) == 0 {
			t.Errorf("%s: empty output", c.name)
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
		{"valid", "a,b\nc,d\n", false},
		{"inconsistent rows", "a,b\nc\n", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got [][]string
		err := csv.New().Unmarshal([]byte(c.data), &got)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestUnmarshal_WrongTarget(t *testing.T) {
	t.Parallel()
	var s string
	err := csv.New().Unmarshal([]byte("a"), &s)
	if !errs.HasReason(err, "VALUE_INVALID") {
		t.Errorf("expected VALUE_INVALID, got %v", err)
	}
}

func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	if _, ok := codec.Lookup(codec.Format("csv")); !ok {
		t.Error("csv codec not registered")
	}
	if _, ok := codec.LookupMIME("text/csv"); !ok {
		t.Error("text/csv not resolved")
	}
	if _, ok := codec.LookupExt(".csv"); !ok {
		t.Error(".csv not resolved")
	}
}
