# secret (service)

The concrete secret stores, the keyring and the rotator (ADR 0096).

```go
store, _ := svcsecret.NewFile(svcsecret.FileConfig{Dir: "/var/lib/app/secrets", Key: kek})
rotator, _ := svcsecret.NewRotator(svcsecret.RotatorConfig{
    Store: store, Name: "session-key",
    Policy: svcsecret.PolicySpec{Every: 30 * 24 * time.Hour, Keep: 3, Generate: svcsecret.Random(32)},
})
_, _ = rotator.Ensure(ctx)
keyring, _ := svcsecret.NewKeyring(store, "session-key")
box, _ := keyring.Seal(ctx, cookie, nil) // opens after a rotation, until pruned
```

- `NewMemory` — in-process.
- `NewEnv` — read-only; `APP_SMTP_URL` or the file `APP_SMTP_URL_FILE` names;
  both set is refused.
- `NewFile` — a 0700 directory, one 0600 record per secret published
  atomically through `vfs`, writers serialised through `lock`, sealed with
  AES-256-GCM when given a key. Unix family only; `UnsupportedPlatform`
  elsewhere.
- `Keyring` — the newest version seals and signs, every kept one opens and
  verifies; boxes carry their version.
- `Rotator` — `Ensure`, `Due`, `RotateIfDue`, `Rotate`, `Run`; keeps at least
  two versions; starts no goroutine.

Public facade: `pkg/v1/secret`. See `CLAUDE.md`.
