// Package buffer provides a pooled *[]byte for zero-alloc formatting in hot
// paths. Consumers acquire a reusable buffer via Get and return it via Put so
// steady-state allocations tend toward zero. A buffer is its caller's from
// [Get] until [Put] and must not be touched after Put. It is a thin byte-slice
// specialisation over internal/kernel/concur/recycler.CappedPool (ADR 0010): the
// recycling mechanism lives in recycler, the 64-KiB capacity threshold and the
// *[]byte type live here.
package buffer
