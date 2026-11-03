# yaml-test-suite — vendored subset

- **Source**: https://github.com/yaml/yaml-test-suite, tag `data-2022-01-17`
  (commit `6e6c296ae9c9d2d5c4134b4b64d01b29ac19ff6f`), the generated data
  branch of release `v2022-01-17`.
- **Licence**: MIT, Copyright (c) 2016-2020 Ingy döt Net — `LICENSE` beside
  this file is the `License` file of tag `v2022-01-17` (commit
  `45db50aecf9b1520f8258938c88f396e96f30831`), byte for byte.
- **Copied unchanged**: for each of the 402 cases (a test ID, or an ID's
  numbered sub-case), the four files the conformance test reads — `===` (the
  case's name), `in.yaml`, `in.json` (the expected value, when the suite gives
  one) and `error` (present for a case the suite declares invalid). The
  event streams, `out.yaml` and `emit.yaml` are not vendored.

`.gitattributes` marks every file `-text`, so no checkout rewrites a line
ending or a byte the suite tests. `conformance_external_test.go`, in the
package above, runs them. To refresh: copy the same four files of every case
from a newer data tag, update this file and the counts the test asserts.
