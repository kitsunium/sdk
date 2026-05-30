# keyenvelope

Password-wrapped symmetric key (DEK) at rest, in a frozen PHC-style grammar.

```go
env, err := keyenvelope.WrapKey(passphrase, dek)   // "$kenv$v=1$kdf=…$…$aead=…$…"
dek, err := keyenvelope.UnwrapKey(passphrase, env)
```

- KEK from PBKDF2-SHA256 (600000 iters, pinned by the kdf id).
- DEK sealed under AES-256-GCM with the header as AAD.
- Unparseable envelope → `InvalidKeyEnvelope`; wrong passphrase →
  `DecryptionFailed`. Transient KEK zeroized on all paths.
