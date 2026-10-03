// Unit tests for the package-page discovery (lib/packages.mjs) and for the
// catalog's handling of the pages it produces (lib/page-catalog.mjs).
// Run via `npm test` (node --test scripts/lib/*.test.mjs).

import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { listPackageDirs, rewriteReadmeLinks } from "./packages.mjs";
import { buildCatalog } from "./page-catalog.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, "..", "..", "..", "..");

/**
 * Builds a pkg/<major>/ tree in a temporary directory: every path in
 * `withReadme` gets a README.md, every path in `bare` is only a directory.
 */
async function fixture(withReadme, bare = []) {
  const root = await mkdtemp(join(tmpdir(), "packages-test-"));
  for (const rel of bare) await mkdir(join(root, rel), { recursive: true });
  for (const rel of withReadme) {
    await mkdir(join(root, rel), { recursive: true });
    await writeFile(join(root, rel, "README.md"), `# ${rel}\n`);
  }
  return root;
}

test("a package under a family directory gets a page at its path", async () => {
  const root = await fixture(
    ["errs", "crypto", "crypto/hash", "data/codec", "data/codec/json"],
    ["data"],
  );
  try {
    assert.deepEqual(await listPackageDirs(root), [
      "crypto",
      "crypto/hash",
      "data/codec",
      "data/codec/json",
      "errs",
    ]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("internal, testdata and dot/underscore directories are never entered", async () => {
  const root = await fixture([
    "app/view",
    "app/view/internal/engine",
    "app/view/testdata/golden",
    "app/.cache",
    "app/_draft",
  ]);
  try {
    assert.deepEqual(await listPackageDirs(root), ["app/view"]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("a missing pkg/<major>/ lists nothing", async () => {
  assert.deepEqual(
    await listPackageDirs(join(tmpdir(), "no-such-pkg-tree")),
    [],
  );
});

test("the repository's own pkg/v1 publishes every README it holds", async () => {
  //: The regression this module exists for: listing only pkg/v1's immediate
  //: children published 4 of the packages once ADR 0155 grouped them.
  const pages = await listPackageDirs(join(REPO_ROOT, "pkg", "v1"));
  for (const rel of [
    "errs",
    "data/codec",
    "observe/logger",
    "observe/logger/writer",
    "app/mail/spool",
  ]) {
    assert.ok(pages.includes(rel), `${rel} has no page`);
  }
  assert.ok(
    !pages.includes("data"),
    "a family directory with no README.md got a page",
  );
});

test("README links reach a package page, or the file on GitHub", () => {
  const blob = "https://github.com/o/r/blob/HEAD";
  const body = [
    "| [errs](./pkg/v1/errs) | [codec](./pkg/v1/data/codec/) | [json](./pkg/v1/data/codec/json) |",
    "[bench](./pkg/v1/data/codec/BENCH.md) [anchor](./pkg/v1/errs#codes) [family](./pkg/v1/data)",
    "[other major](./pkg/v2/errs) [license](./LICENSE) [git](./framework/git)",
    "[abs](https://example.com/x) [here](#install) [rel](docs/adr/)",
  ].join("\n");
  const out = rewriteReadmeLinks(
    body,
    "v1",
    ["errs", "data/codec", "data/codec/json"],
    blob,
  );
  assert.equal(
    out,
    [
      "| [errs](./errs/) | [codec](./data/codec/) | [json](./data/codec/json/) |",
      `[bench](${blob}/pkg/v1/data/codec/BENCH.md) [anchor](${blob}/pkg/v1/errs#codes) [family](${blob}/pkg/v1/data)`,
      `[other major](${blob}/pkg/v2/errs) [license](${blob}/LICENSE) [git](${blob}/framework/git)`,
      "[abs](https://example.com/x) [here](#install) [rel](docs/adr/)",
    ].join("\n"),
  );
});

test("the catalog lists a package under a family and skips a reserved section's pages", () => {
  const ids = [
    "local/v1",
    "local/v1/getting-started",
    "local/v1/errs",
    "local/v1/data/codec",
    "local/v1/data/codec/json",
    "local/v1/adr",
    "local/v1/adr/0001-foo",
    "local/v1/changelog",
  ];
  const catalog = buildCatalog(
    ids.map((id) => ({ id })),
    "local/v1",
    "/local/v1",
  );
  const packages = catalog.find((g) => g.group === "Packages");
  assert.ok(packages, "no Packages group");
  assert.deepEqual(
    packages.items.map((i) => [i.label, i.href]),
    [
      ["data/codec", "/local/v1/data/codec/"],
      ["data/codec/json", "/local/v1/data/codec/json/"],
      ["errs", "/local/v1/errs/"],
    ],
  );
  const everyHref = catalog.flatMap((g) => g.items.map((i) => i.href));
  assert.ok(
    !everyHref.includes("/local/v1/adr/0001-foo/"),
    "a reserved section's page reached the catalog",
  );
});
