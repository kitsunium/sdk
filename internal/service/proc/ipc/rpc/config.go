package rpc

import (
	"slices"
	"time"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// bounds are the three bounds both ends share.
type bounds struct {
	// maxFrame, inFlight and frameTimeout are the configuration's.
	maxFrame, inFlight int
	frameTimeout       time.Duration
}

// check refuses a bound that is not positive, or a frame bound a u32 cannot
// say or no call fits in.
func (b bounds) check() error {
	switch {
	case b.maxFrame < minFrameBytes:
		return misconfigured("MaxFrameBytes below the smallest frame a call fits in")
	case int64(b.maxFrame) > maxFrameLimit:
		return misconfigured("MaxFrameBytes past what a u32 length says")
	case b.inFlight <= 0:
		return misconfigured("MaxInFlight not positive")
	case b.frameTimeout <= 0:
		return misconfigured("FrameTimeout not positive")
	}
	return nil
}

// checkFormat refuses no format, one longer than a hello carries, and one no
// codec is registered under; it returns the codec.
func checkFormat(f codec.Format) (codec.Codec, error) {
	//: the format travels in the hello's u8-length name.
	if f == "" || len(f) > maxNameBytes {
		return nil, misconfigured("a format empty or longer than 255 bytes")
	}
	c, ok := codec.Lookup(f)
	//: nothing registered under that name: its package was not imported.
	if !ok {
		return nil, misconfigured("a format no codec is registered under")
	}
	return c, nil
}

// serverCodecs validates cfg and looks up the codec of each format.
func serverCodecs(cfg ServerConfig) (map[codec.Format]codec.Codec, error) {
	//: the bounds first: they are the caller's decision.
	if err := (bounds{cfg.MaxFrameBytes, cfg.MaxInFlight, cfg.FrameTimeout}).check(); err != nil {
		return nil, err
	}
	//: a server that speaks no format answers nobody.
	if len(cfg.Formats) == 0 {
		return nil, misconfigured("no format")
	}
	codecs := make(map[codec.Format]codec.Codec, len(cfg.Formats))
	for _, f := range slices.Clone(cfg.Formats) {
		c, err := checkFormat(f)
		//: every format must be speakable.
		if err != nil {
			return nil, err
		}
		codecs[f] = c
	}
	return codecs, nil
}

// clientCodec validates cfg and looks up its format's codec.
func clientCodec(cfg ClientConfig) (codec.Codec, error) {
	//: the bounds first: they are the caller's decision.
	if err := (bounds{cfg.MaxFrameBytes, cfg.MaxInFlight, cfg.FrameTimeout}).check(); err != nil {
		return nil, err
	}
	//: a call that may wait for ever has no bound.
	if cfg.CallTimeout <= 0 {
		return nil, misconfigured("CallTimeout not positive")
	}
	return checkFormat(cfg.Format)
}

// clockOr is clk, or clock.System for nil.
func clockOr(clk clock.Timed) clock.Timed {
	//: nil is the documented default: the wall clock.
	if clk == nil {
		return clock.System
	}
	return clk
}
