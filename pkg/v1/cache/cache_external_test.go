package cache_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/cache"
)

// TestFacade smoke-tests the public alias + constructor.
func TestFacade(t *testing.T) {
	t.Parallel()
	c := cache.New[string, int](cache.Config[string, int]{MaxEntries: 8, DefaultTTL: time.Minute})
	c.Set("answer", 42)
	//: a freshly-set key fetches back its value.
	if v, ok := c.Fetch("answer"); !ok || v != 42 {
		t.Fatalf("Fetch=%d,%v want 42,true", v, ok)
	}
	//: Stats (public alias) reflects the single hit.
	if st := c.Stats(); st.Hits != 1 {
		t.Errorf("Hits=%d, want 1", st.Hits)
	}
}
