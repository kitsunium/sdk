package service

import "github.com/kitsunium/sdk/internal/kernel/clock"

// Clock is the time every wait of a unit runs on: the kernel's clock.Timed,
// so pkg/v1/clock's System and ManualClock are clocks here (ADR 0214 §5).
type Clock = clock.Timed
