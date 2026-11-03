# bson-corpus — vendored, with attribution

- **Work**: the BSON corpus of the MongoDB Specifications — the per-type
  `*.json` test files of `source/bson-corpus/tests/`.
- **Source**: https://github.com/mongodb/specifications, commit
  `775c4bce1bfe1c409b947eae1f3eab6975fccfe3` (2026-10-27), path
  `source/bson-corpus/tests/*.json`.
- **Licensor**: MongoDB, Inc. and the contributors to mongodb/specifications.
- **Licence**: Creative Commons Attribution-NonCommercial-ShareAlike 3.0
  United States (CC BY-NC-SA 3.0 US), https://creativecommons.org/licenses/by-nc-sa/3.0/us/
  — the full text is `LICENSE` beside this file, a byte-for-byte copy of the
  upstream `LICENSE.md`.
- **Changes**: none. The 31 JSON files are copied byte for byte; the
  repository's other files and the `bsonview` script are not vendored.

The licence covers these test files only, not the SDK's code that reads them.
They stay under CC BY-NC-SA 3.0 US wherever this directory is redistributed:
no commercial use of the corpus, the attribution above kept, and any
adaptation of the files shared under the same licence.

`conformance_external_test.go`, in the package above, runs them. To refresh:
copy the same files from a newer commit, update the commit line above and the
counts the test asserts.
