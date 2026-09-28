<!-- updated: 2026-09-29T00:30:00Z -->
# internal/service/crypto/commonpw/

## Purpose

The ten thousand most common passwords, and `IsCommon`, which says whether a
password is one of them, case-insensitively (ADR 0143 §D10). It is the
blocklist NIST SP 800-63B-4 requires a verifier to check a new password
against, and more than the top 3 000 OWASP ASVS 5.0 (6.2.4) asks for. Public
facade: `pkg/v1/password` (`IsCommon`). A framework's `NotCommon` password
option (kit's ADR 0007 §2) reads it.

Data, not a scheme: it registers nothing and mints no code — a lookup cannot
fail.

## Source and licence

| | |
|---|---|
| File | `xato-net-10-million-passwords-10000.txt`, byte for byte |
| From | SecLists, https://github.com/danielmiessler/SecLists, `Passwords/Common-Credentials/` |
| Commit | `2e3e92569043d24297ca6c35070078e5cf41651e` (2026-09-28) |
| SHA-256 | `c63d5e4ccc31344d662583cc39ca4bd5bd20517ff1d24501f0c4e0c22d9b722a` — `TestTheListLoads` checks it |
| Licence | MIT, Copyright (c) 2018 Daniel Miessler — `LICENSE.SecLists`, beside the list, travels with it in the module |
| Provenance | the 10 000 most frequent passwords of the ten million credentials Mark Burnett released into the public domain in 2015 (xato.net; the Internet Archive item `10MillionPasswords` carries the Public Domain Mark) |

To refresh it: fetch the same path at a newer commit, replace the file, and
update the commit and the digest here, in the package doc and in the test —
the three must agree, and the test fails until they do.

## Contents

| File | Role |
|---|---|
| `commonpw.go` | package doc (source, licence, comparison), the embedded `listText`, `sorted` (lower-cased, deduplicated, sorted once, at the first question), `IsCommon`, `Len` |
| `xato-net-10-million-passwords-10000.txt` | the list, 10 000 lines, one of them empty |
| `LICENSE.SecLists` | SecLists' MIT licence |
| `.gitattributes` | both files `-text`: a Windows checkout with `core.autocrlf` would otherwise rewrite the list's line endings, and its digest with them — found by the `windows` lane |

## Why-this-shape

- **Case-insensitive, nothing else normalised.** A guessing attacker tries case
  variants first; refusing `PASSWORD` with `password` costs a person nothing.
  No trimming and no look-alike substitution: NIST says a verifier SHALL NOT
  alter a password, and a substitution table is a policy, not a list.
- **The empty password is a length rule's.** The file holds it — one empty line
  — and `IsCommon` answers false for it, so the list is 9 999 passwords, 9 916
  once case variants are one.
- **Sorted once, searched after.** A binary search over substrings of the
  embedded text: no map of ten thousand keys, and nothing built until somebody
  asks.
- **Byte for byte, pinned.** The file is SecLists' own, so its provenance is
  checkable with one `sha256sum`; the lower-casing and sorting happen in
  memory.

## Do NOT

- Edit the list by hand: replace it with a newer SecLists file, and repin.
- Add a list without a licence that allows embedding it.

## Verification

```
bazel test --config=race //internal/service/crypto/commonpw:commonpw_test
cd internal/service && GOWORK=off go test -race ./crypto/commonpw/
```
