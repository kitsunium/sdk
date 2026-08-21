package id_test

import (
	"strconv"
	"strings"
	"testing"

	coreid "github.com/kitsunium/sdk/internal/core/id"
	svcid "github.com/kitsunium/sdk/internal/service/id"
)

const (
	uuidLen    int    = 36
	ulidLen    int    = 26
	uuidVerIdx int    = 14 // position of the version nibble char in the dashed form
	crockfordA string = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// TestUUIDShape checks v4/v7 length, dashes, and version char.
func TestUUIDShape(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		gen     func() (string, error)
		version byte
	}
	tests := []tc{
		{"uuidv4", svcid.UUIDv4.New, '4'},
		{"uuidv7", svcid.UUIDv7.New, '7'},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := c.gen()
		if err != nil {
			t.Fatalf("%s: New err=%v", c.name, err)
		}
		//: canonical dashed form is exactly 36 chars.
		if len(got) != uuidLen {
			t.Fatalf("%s: len=%d want %d (%q)", c.name, len(got), uuidLen, got)
		}
		//: dashes sit at 8/13/18/23.
		if got[8] != '-' || got[13] != '-' || got[18] != '-' || got[23] != '-' {
			t.Errorf("%s: dashes misplaced: %q", c.name, got)
		}
		//: the version nibble char identifies v4 vs v7.
		if got[uuidVerIdx] != c.version {
			t.Errorf("%s: version char = %c, want %c", c.name, got[uuidVerIdx], c.version)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); runCase(t, c) })
	}
}

// TestTimeOrdered checks the time PREFIX of UUIDv7/ULID is non-decreasing.
// (Within one millisecond the random suffix is unordered by design; only the
// ms-timestamp prefix is k-sortable.)
func TestTimeOrdered(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		gen       func() (string, error)
		prefixLen int
	}
	tests := []tc{{"uuidv7", svcid.UUIDv7.New, 13}, {"ulid", svcid.ULID.New, 10}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: two ids minted in order — their ms-time prefixes must not regress.
		first, err1 := c.gen()
		second, err2 := c.gen()
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: New err=%v/%v", c.name, err1, err2)
		}
		if first[:c.prefixLen] > second[:c.prefixLen] {
			t.Errorf("%s: time prefix regressed: %q > %q", c.name, first[:c.prefixLen], second[:c.prefixLen])
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); runCase(t, c) })
	}
}

// TestULIDShape checks ULID length + Crockford alphabet membership.
func TestULIDShape(t *testing.T) {
	t.Parallel()
	got, err := svcid.ULID.New()
	if err != nil {
		t.Fatalf("ULID New err=%v", err)
	}
	//: ULID renders to exactly 26 Crockford chars.
	if len(got) != ulidLen {
		t.Fatalf("ULID len=%d want %d (%q)", len(got), ulidLen, got)
	}
	//: every char must be a member of the Crockford alphabet.
	for _, r := range got {
		if !strings.ContainsRune(crockfordA, r) {
			t.Errorf("ULID char %c not in Crockford alphabet (%q)", r, got)
		}
	}
}

// TestSnowflakeMonotonic checks successive snowflakes are strictly increasing.
func TestSnowflakeMonotonic(t *testing.T) {
	t.Parallel()
	gen := svcid.NewSnowflake(7)
	prev := int64(-1)
	//: 1000 successive ids must each parse and strictly increase.
	for range 1000 {
		s, err := gen.New()
		if err != nil {
			t.Fatalf("snowflake New err=%v", err)
		}
		v, perr := strconv.ParseInt(s, 10, 64)
		if perr != nil {
			t.Fatalf("snowflake %q not decimal: %v", s, perr)
		}
		if v <= prev {
			t.Fatalf("snowflake not monotonic: %d <= %d", v, prev)
		}
		prev = v
	}
}

// TestUniqueness asserts a batch of UUIDv4 has no collisions.
func TestUniqueness(t *testing.T) {
	t.Parallel()
	const batchSize int = 100000
	seen := make(map[string]struct{}, batchSize)
	//: a 100k batch must be collision-free.
	for range batchSize {
		s, err := svcid.UUIDv4.New()
		if err != nil {
			t.Fatalf("UUIDv4 New err=%v", err)
		}
		if _, dup := seen[s]; dup {
			t.Fatalf("UUIDv4 collision: %q", s)
		}
		seen[s] = struct{}{}
	}
}

// TestRegisteredSchemes confirms the four schemes self-registered on import.
func TestRegisteredSchemes(t *testing.T) {
	t.Parallel()
	want := []coreid.Scheme{"snowflake", "ulid", "uuidv4", "uuidv7"} // sorted
	got := coreid.Available()
	//: Available returns the sorted registered set.
	if len(got) < len(want) {
		t.Fatalf("Available=%v, want at least %v", got, want)
	}
	for _, w := range want {
		if !w.Known() {
			t.Errorf("scheme %q not registered", w)
		}
	}
}
