# keytree

Path-addressed hierarchical key derivation over HKDF.

```go
t := keytree.NewKeyTree(algo, master)
k, err := t.Child("svc").Child("db").DeriveKey()
```

- Re-derives from master with the full canonical path as HKDF info.
- Injective path encoding (length-prefixed segments) — distinct paths never
  collide.
- `Child` is pure; `DeriveKey` yields a 32-byte Key. The root owns the master
  Zeroize lifetime.
