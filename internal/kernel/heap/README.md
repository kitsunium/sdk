# heap

Generic binary heap `Heap[T]` for the SDK kernel — stdlib-only, no domain
vocabulary. One type parameter and one comparison function; no `Job`, no
`Priority`, no interface to implement.

```go
import (
    "cmp"

    "github.com/kitsunium/sdk/internal/kernel/heap"
)

h := heap.New(cmp.Compare[int]) // min-heap; reverse the arguments for a max-heap
h.Push(5)
h.Push(1)

top, ok := h.Pop() // 1, true
_, ok = h.Peek()   // pure read: peeking does not mutate
```

It exists because `container/heap` is not generic: it is an algorithm over a
five-method interface, so every use site writes boilerplate, boxes each element
into `any`, and type-asserts it back out. `BENCH.md` measures the difference —
**1.75× the time and one 16 B allocation per push**.

Edges worth knowing:

- **`New(nil)` panics.** There is no order the SDK can invent for an arbitrary
  `T`, so the missing comparison is refused at the call that omitted it rather
  than at the first `Push` (ADR 0031, refusal branch).
- **`Pop` / `Peek` return `(T, bool)`** — an empty heap is a state, not a fault.
- **Ties pop in an unspecified order.** A binary heap is not stable; add a
  sequence number to `T` if you need FIFO among equals.
- **Not safe for concurrent use.** The caller picks the lock.

See `CLAUDE.md` for the design and `BENCH.md` for the numbers.
