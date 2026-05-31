# encwrite

```go
import "github.com/kitsunium/sdk/internal/service/logger/middleware/encwrite"
```

Logger middleware that seals each record's bytes under a per-sink subkey
(HKDF-SHA256 derived from the configured master key) with AES-256-GCM, then
writes the length-prefixed sealed box to a downstream `core/logger.Sink`.

`Wrap(cfg, downstream)` returns the wrapping `Sink`. The framing is
`[4-byte big-endian length][sealed box]`. `Close` zeroizes both the master key
and the derived subkey.

Requires blank-importing the AEAD and deriver implementations:

```go
import (
	_ "github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
	_ "github.com/kitsunium/sdk/internal/service/crypto/hkdfsha256"
)
```
