// Package buffer provides a pooled *[]byte for zero-alloc formatting in hot
// paths. Consumers acquire a reusable buffer via Get or GetSize and return it
// via Put so steady-state allocations tend toward zero. A buffer is its
// caller's from [Get] until [Put] and must not be touched after Put. It is a
// thin byte-slice specialisation over
// internal/kernel/concur/recycler.CappedPool (ADR 0010): the recycling
// mechanism lives in recycler, the size classes and the *[]byte type live
// here.
//
// Size classes (#810, ADR 0216): five pools, of 1, 4, 16, 64 and 256 KiB
// buffers. GetSize serves a request from the smallest class that fits, and
// Put routes a buffer by its capacity to the largest class it covers, which
// keeps it only while its capacity is at most twice the class's size — a
// buffer grown past that is dropped, so no class pins more than twice what
// it hands out, and a 1 KiB caller is never handed a 64 KiB buffer another
// returned.
package buffer
