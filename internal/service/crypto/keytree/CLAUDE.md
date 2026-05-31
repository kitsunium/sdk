# internal/service/crypto/keytree

Path-addressed hierarchical key derivation — a pure composition over the
registered HKDF Deriver. Mints no codes (derivation failures forward the core
`DerivationFailed`).

## API

```go
t := keytree.NewKeyTree(algo, master)
k, err := t.Child("svc").Child("db").DeriveKey()  // 32-byte AEAD Key
```

## Injective path encoding (locked correctness)

The HKDF `info` is the concatenation, per segment, of a 4-byte big-endian
length prefix followed by the segment bytes. This makes the encoding
prefix-free, so `Child("a/b").Child("c")` and `Child("a").Child("b/c")` derive
**different** keys — the length prefix removes any "/" ambiguity.

## Model

- **Re-derive from master.** `DeriveKey` calls `Subkey(algo, master, nil,
  canonicalInfo, KeyLen)` — never a chained derive — so any node is
  reconstructible from the root.
- **Immutable.** `Child` copies the path slice and returns a new value; the
  receiver is untouched. The master Key is shared by reference; the **root
  owns the Zeroize lifetime** — zeroizing the master invalidates all nodes.
- **Self-activating.** Blank-imports `hkdfsha256` so the deriver is registered.
