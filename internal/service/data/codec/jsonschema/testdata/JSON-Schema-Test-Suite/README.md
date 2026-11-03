# JSON-Schema-Test-Suite — vendored subset

Upstream: https://github.com/json-schema-org/JSON-Schema-Test-Suite, commit
`ed0ebd31d0a83fd79e99a32aa0c884b966ada057` (2026-11-02), MIT licence (`LICENSE`).

Copied unchanged: `tests/draft2020-12/*.json` (the required tests; `optional/`
is not vendored) and `remotes/draft2020-12/`, which the suite serves at
`http://localhost:1234/draft2020-12/` and `suite_external_test.go` registers
under those URIs through `Config.Resources`. Refresh by copying the same paths
from a newer commit and updating this line and the skip list.
