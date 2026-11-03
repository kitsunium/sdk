package cache

import (
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
)

// Config configures NewStore: the client, the namespace, the default
// lifetime, the codec values travel in, and the cross-replica fill lease.
type Config struct {
	// Client is the caller's go-redis client — a single node, a sentinel
	// failover or a cluster. The store never closes it. Required.
	Client goredis.UniversalClient
	// Codec encodes and decodes the values. Required.
	Codec codec.Codec
	// Clock times the pollers' waits. Nil means clock.System. An entry's
	// deadline is the server's, never this clock's.
	Clock clock.Timed
	// Prefix namespaces every key and is their hash tag, so it must be
	// non-empty and hold no brace. Required.
	Prefix string
	// DefaultTTL applies to an entry whose own TTL is zero; zero means no
	// deadline, as for the memory store. Negative is refused.
	DefaultTTL time.Duration
	// FillLease is how long the cross-replica fill lease of Load lasts. Zero
	// keeps Load's collapsing within the process, the port's own promise;
	// negative is refused.
	FillLease time.Duration
	// FillPoll is how often a caller that lost the fill lease looks for the
	// winner's value. Required, positive and shorter than FillLease, when
	// FillLease is positive.
	FillPoll time.Duration
}

// validate refuses, by field, a Config no store can be built from.
func (c *Config) validate() error {
	//: the client, the codec and the key space first, in declaration order.
	if handleErr := c.validateHandles(); handleErr != nil {
		//: named.
		return handleErr
	}
	//: then the lifetimes.
	return c.validateLifetimes()
}

// validateHandles refuses a missing client or codec, and a prefix that is
// empty or holds a brace.
func (c *Config) validateHandles() error {
	//: the first field that is wrong, in declaration order.
	switch {
	case c.Client == nil:
		//: named.
		return misconfigured("Client")
	case c.Codec == nil:
		//: named.
		return misconfigured("Codec")
	//: the prefix is the hash tag: a brace inside it would end the tag early
	//: and scatter one store's keys over several slots.
	case c.Prefix == "" || strings.ContainsAny(c.Prefix, "{}"):
		//: named.
		return misconfigured("Prefix")
	default:
		//: a store can reach its server.
		return nil
	}
}

// validateLifetimes refuses a negative DefaultTTL or FillLease, and a
// FillPoll that cannot see the winner before a positive FillLease lapses.
func (c *Config) validateLifetimes() error {
	//: the first field that is wrong, in declaration order.
	switch {
	case c.DefaultTTL < 0:
		//: named.
		return misconfigured("DefaultTTL")
	case c.FillLease < 0:
		//: named.
		return misconfigured("FillLease")
	//: a lease asks for a poll that can see the winner before it lapses.
	case c.FillLease > 0 && (c.FillPoll <= 0 || c.FillPoll >= c.FillLease):
		//: named.
		return misconfigured("FillPoll")
	default:
		//: a store can be built.
		return nil
	}
}

// misconfigured refuses the configuration, naming the field at fault.
func misconfigured(field string) error {
	//: origin wins: the domain's sentinel, the field as a diagnostic.
	return errs.Wrap(corecache.CacheMisconfigured, errs.WrapParams{}, errs.String("field", field))
}

// clockOrSystem is the configured clock, the system's when none is.
func (c *Config) clockOrSystem() clock.Timed {
	//: nil means the wall clock.
	if c.Clock == nil {
		//: package time.
		return clock.System
	}
	//: the caller's, a test's ManualClock included.
	return c.Clock
}
