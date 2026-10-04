// Unit tests for the docs/api reader (lib/api.mjs): the go: ids against the
// vectors docs/api/schema.json ships, the declarations, the doc text, the page
// models and the ⌘K entries over a fixture — and the repository's own
// docs/api, whose pkg/v1 every package page and every alias member must reach.
// Run via `npm test` (node --test scripts/lib/*.test.mjs).

import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, mkdir, writeFile, rm, readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  API_FORMAT,
  apiPages,
  declOf,
  docBlocks,
  inlineSegments,
  isDeprecated,
  isPointerMethod,
  oneLine,
  pageAnchors,
  pageOf,
  readApi,
  splitGoID,
  symbolEntries,
  synopsis,
  typeDecl,
} from "./api.mjs";
import { listPackageDirs } from "./packages.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, "..", "..", "..", "..");

// ─── go: ids ───────────────────────────────────────────────────────

test("go: ids split as schema.json's vectors say", async () => {
  const schema = JSON.parse(
    await readFile(join(REPO_ROOT, "docs", "api", "schema.json"), "utf8"),
  );
  const vectors = schema["x-vectors"].goid.valid;
  assert.ok(vectors.length > 10);
  for (const v of vectors) {
    assert.equal(splitGoID(v.id).path, v.path, v.id);
    assert.equal(isPointerMethod(v.id), v.pointer === true, v.id);
  }
});

// ─── Declarations ──────────────────────────────────────────────────

test("each kind is declared as gofmt starts it", () => {
  const rec = (kind, spelled, extra = {}) => ({
    id: "go:example.com/p.X",
    kind,
    name: "X",
    spelled,
    ...extra,
  });
  assert.equal(
    declOf(rec("func", "func(cfg Config) (Locker, error)")),
    "func X(cfg Config) (Locker, error)",
  );
  assert.equal(
    declOf(rec("func", "func[T any](fn func(*T)) Opt")),
    "func X[T any](fn func(*T)) Opt",
  );
  assert.equal(
    declOf(
      rec("method", "func() string", {
        id: "go:example.com/p.Code.String",
        recv: "Code",
        name: "String",
      }),
    ),
    "func (Code) String() string",
  );
  assert.equal(
    declOf(
      rec("method", "func(n int) error", {
        id: "go:example.com/p.(*T).Grow",
        recv: "T",
        name: "Grow",
      }),
      { recv: "Alias" },
    ),
    "func (*Alias) Grow(n int) error",
  );
  assert.equal(
    declOf(rec("alias", "[K comparable, V any] cache.Cache[K, V]")),
    "type X[K comparable, V any] = cache.Cache[K, V]",
  );
  assert.equal(
    declOf(rec("const", "errs.Code", { value: "136450" })),
    "const X errs.Code = 136450",
  );
  assert.equal(
    declOf(rec("const", "untyped string", { value: '"json"' })),
    'const X = "json"',
  );
  assert.equal(declOf(rec("var", "*errs.Error")), "var X *errs.Error");
  assert.equal(declOf(rec("type", "string")), "type X string");
});

test("a struct and an interface are written over several lines", () => {
  assert.equal(
    typeDecl(
      "Config",
      'struct{Name string "json:\\"name\\""; TTL time.Duration; Base}',
      [
        { name: "Name", type: "string", tag: 'json:"name"' },
        { name: "TTL", type: "time.Duration" },
        { name: "Base", type: "Base", embedded: true },
      ],
    ),
    'type Config struct {\n\tName string `json:"name"`\n\tTTL  time.Duration\n\tBase\n}',
  );
  assert.equal(
    typeDecl(
      "Locker",
      "interface{Acquire(ctx context.Context, name string) (Lease, error); io.Closer; sealed()}",
    ),
    "type Locker interface {\n\tAcquire(ctx context.Context, name string) (Lease, error)\n\tio.Closer\n\tsealed()\n}",
  );
  assert.equal(
    typeDecl("Box", "[T any] struct{}"),
    "type Box[T any] struct {\n\t// no exported field\n}",
  );
  assert.equal(typeDecl("Any", "interface{}"), "type Any interface{}");
  assert.equal(
    oneLine("type Locker interface {\n\tA()\n\tB()\n}"),
    "type Locker interface { A(); B() }",
  );
});

// ─── Doc text ──────────────────────────────────────────────────────

test("a doc splits into paragraphs, headings, code and lists", () => {
  const text = [
    "Package lock hands out leases,",
    "named and exclusive.",
    "",
    "# Read this first",
    "",
    "\tlease, err := locker.Acquire(ctx, name)",
    "",
    "\tdefer lease.Release(ctx)",
    "",
    "The rules:",
    "  - a lease expires;",
    "    a file lease does not.",
    "  - a fence grows.",
    "",
    "In order:",
    "  1. first",
    "  2. second",
    "",
  ].join("\n");
  assert.deepEqual(docBlocks(text), [
    { type: "p", text: "Package lock hands out leases, named and exclusive." },
    { type: "h", text: "Read this first" },
    {
      type: "code",
      text: "lease, err := locker.Acquire(ctx, name)\n\ndefer lease.Release(ctx)",
    },
    { type: "p", text: "The rules:" },
    {
      type: "list",
      ordered: false,
      items: ["a lease expires; a file lease does not.", "a fence grows."],
    },
    { type: "p", text: "In order:" },
    { type: "list", ordered: true, items: ["first", "second"] },
  ]);
  assert.deepEqual(docBlocks(undefined), []);
});

test("doc links resolve on the page, read as text elsewhere, and brackets that are none stay", () => {
  const resolve = (t) => (["Lease", "Lease.Extend"].includes(t) ? t : null);
  assert.deepEqual(
    inlineSegments("See [Lease], [Lease.Extend] and [errs.Code].", resolve),
    [
      { text: "See " },
      { text: "Lease", href: "#Lease" },
      { text: ", " },
      { text: "Lease.Extend", href: "#Lease.Extend" },
      { text: " and errs.Code." },
    ],
  );
  assert.deepEqual(
    inlineSegments("m[key] and [0, n) and map[ast.Expr]T", resolve),
    [{ text: "m[key] and [0, n) and map[ast.Expr]T" }],
  );
  assert.deepEqual(
    inlineSegments("RFC at https://www.rfc-editor.org/rfc/rfc6455.", resolve),
    [
      { text: "RFC at " },
      {
        text: "https://www.rfc-editor.org/rfc/rfc6455",
        href: "https://www.rfc-editor.org/rfc/rfc6455",
      },
      { text: "." },
    ],
  );
  assert.deepEqual(
    inlineSegments("[encoding/json.Marshal] writes it", resolve),
    [{ text: "encoding/json.Marshal writes it" }],
  );
});

test("a synopsis is go/doc's first sentence, and a deprecation is a paragraph", () => {
  assert.equal(
    synopsis("NewMemory returns a [Locker]. It expires.\n"),
    "NewMemory returns a Locker.",
  );
  assert.equal(
    synopsis("Code is the U.S. law. Then more."),
    "Code is the U.S. law.",
  );
  assert.equal(
    synopsis("One line without a period"),
    "One line without a period",
  );
  assert.equal(synopsis("\tcode first\n"), "");
  assert.equal(synopsis(""), "");
  assert.equal(isDeprecated("Old does it.\n\nDeprecated: use New.\n"), true);
  assert.equal(
    isDeprecated("Deprecated is a word here: not a marker.\n"),
    false,
  );
});

// ─── Pages and ⌘K ──────────────────────────────────────────────────

const ROOT = "example.com/sdk";
const P = `${ROOT}/pkg/v1`;
const CORE = `${ROOT}/internal/core/lock`;
const SVC = `${ROOT}/internal/service/lock`;
const CELLS = ["linux/amd64", "linux/386", "windows/amd64"];

function sym(pkg, kind, name, spelled, extra = {}) {
  const id = extra.id ?? `go:${pkg}.${name}`;
  return {
    id,
    kind,
    package: pkg,
    name,
    spelled,
    canonical: spelled,
    file: "x.go",
    ...extra,
  };
}

/** The root module's document and a vendor module's. */
function fixtureDocuments() {
  const root = {
    format: API_FORMAT,
    module: ROOT,
    dir: ".",
    cells: CELLS,
    packages: [
      { path: CORE, name: "lock", dir: "internal/core/lock" },
      { path: SVC, name: "lock", dir: "internal/service/lock" },
      { path: `${P}/lock`, name: "lock", dir: "pkg/v1/lock" },
      {
        path: `${P}/lock/internal/impl`,
        name: "impl",
        dir: "pkg/v1/lock/internal/impl",
      },
      { path: `${P}/group`, name: "group", dir: "pkg/v1/group" },
      { path: `${P}/empty`, name: "empty", dir: "pkg/v1/empty" },
    ],
    symbols: [
      sym(CORE, "type", "Locker", "interface{Acquire(name string) error}", {
        doc: "Locker hands out leases.\n",
      }),
      sym(CORE, "method", "Acquire", "func(name string) error", {
        id: `go:${CORE}.Locker.Acquire`,
        recv: "Locker",
        doc: "Acquire blocks until [Locker] is held.\n",
      }),
      sym(SVC, "type", "Config", "struct{TTL time.Duration; Path string}", {
        fields: [
          {
            name: "TTL",
            type: "time.Duration",
            doc: "TTL must be positive.\n",
          },
        ],
        platforms: ["linux/amd64", "linux/386"],
      }),
      sym(SVC, "type", "Config", "struct{TTL time.Duration; Path string}", {
        fields: [
          {
            name: "TTL",
            type: "time.Duration",
            doc: "TTL must be positive.\n",
          },
          { name: "Path", type: "string" },
        ],
        platforms: ["windows/amd64"],
      }),
      sym(SVC, "method", "Valid", "func() bool", {
        id: `go:${SVC}.(*Config).Valid`,
        recv: "Config",
      }),
      sym(`${P}/group`, "const", "Unlimited", "int", {
        value: "9223372036854775807",
        platforms: ["linux/amd64", "windows/amd64"],
      }),
      sym(`${P}/group`, "const", "Unlimited", "int", {
        value: "2147483647",
        platforms: ["linux/386"],
      }),
      sym(`${P}/lock`, "alias", "Config", "svclock.Config", {
        owner: `go:${SVC}.Config`,
      }),
      sym(`${P}/lock`, "alias", "FS", "corevfs.FS", { owner: "go:io/fs.FS" }),
      sym(`${P}/lock`, "var", "LockNotHeld", "*errs.Error", {
        init: `go:${CORE}.LockNotHeld`,
        code: {
          value: "0.2.21.2",
          reason: "LOCK_NOT_HELD",
          public: "The lease is no longer held",
        },
        doc: "LockNotHeld is returned by Extend.\n\nDeprecated: match the code.\n",
      }),
      sym(`${P}/lock`, "alias", "Locker", "corelock.Locker", {
        owner: `go:${CORE}.Locker`,
      }),
      sym(
        `${P}/lock`,
        "func",
        "NewMemory",
        "func(cfg Config) (Locker, error)",
        {
          doc: "NewMemory returns a [Locker]. It expires.\n",
        },
      ),
      sym(`${P}/lock`, "type", "Policy", "int"),
      sym(`${P}/lock`, "method", "String", "func() string", {
        id: `go:${P}/lock.Policy.String`,
        recv: "Policy",
      }),
      sym(`${P}/lock/internal/impl`, "func", "Hidden", "func()"),
    ],
  };
  const vendor = {
    format: API_FORMAT,
    module: `${ROOT}/third-party/aws`,
    dir: "third-party/aws",
    cells: CELLS,
    packages: [{ path: `${ROOT}/third-party/aws`, name: "aws", dir: "." }],
    symbols: [sym(`${ROOT}/third-party/aws`, "func", "New", "func() *Writer")],
  };
  return [root, vendor];
}

test("a package page holds its symbols, an alias its owner's members", () => {
  const pages = apiPages(fixtureDocuments(), "v1");
  assert.deepEqual([...pages.keys()], ["empty", "group", "lock"]);
  assert.deepEqual(
    pages.get("empty").sections.map((s) => s.symbols.length),
    [0, 0, 0, 0],
  );

  const lock = pages.get("lock");
  assert.equal(lock.package.dir, "pkg/v1/lock");
  const names = lock.sections.map((s) => s.symbols.map((x) => x.name));
  assert.deepEqual(names, [
    [],
    ["LockNotHeld"],
    ["NewMemory"],
    ["Config", "FS", "Locker", "Policy"],
  ]);

  const [config, fs, locker, policy] = lock.sections[3].symbols;
  assert.equal(config.forms[0].decl, "type Config = svclock.Config");
  assert.equal(config.owner.path, SVC);
  assert.deepEqual(
    config.owner.forms.map((f) => f.platforms),
    [["linux/amd64", "linux/386"], ["windows/amd64"]],
  );
  //: a field every form has carries no cells; one a form lacks names its own.
  assert.deepEqual(config.fields, [
    {
      anchor: "Config.TTL",
      name: "TTL",
      type: "time.Duration",
      doc: "TTL must be positive.\n",
    },
    {
      anchor: "Config.Path",
      name: "Path",
      type: "string",
      platforms: ["windows/amd64"],
    },
  ]);
  assert.deepEqual(
    config.methods.map((m) => [m.anchor, m.forms[0].decl]),
    [["Config.Valid", "func (*Config) Valid() bool"]],
  );
  assert.deepEqual(fs.owner, {
    id: "go:io/fs.FS",
    path: "io/fs",
    name: "FS",
    external: true,
  });
  assert.deepEqual(
    locker.methods.map((m) => [m.anchor, m.forms[0].decl, m.forms[0].file]),
    [["Locker.Acquire", "func (Locker) Acquire(name string) error", "x.go"]],
  );
  assert.deepEqual(
    policy.methods.map((m) => m.anchor),
    ["Policy.String"],
  );
  const notHeld = lock.sections[1].symbols[0];
  assert.deepEqual(notHeld.forms[0].code, {
    value: "0.2.21.2",
    reason: "LOCK_NOT_HELD",
    public: "The lease is no longer held",
  });
  assert.equal(notHeld.forms[0].init, `go:${CORE}.LockNotHeld`);

  const unlimited = pages.get("group").sections[0].symbols[0];
  assert.deepEqual(
    unlimited.forms.map((f) => [f.decl, f.platforms]),
    [
      [
        "const Unlimited int = 9223372036854775807",
        ["linux/amd64", "windows/amd64"],
      ],
      ["const Unlimited int = 2147483647", ["linux/386"]],
    ],
  );
  assert.deepEqual([...pageAnchors(lock)].sort(), [
    "Config",
    "Config.Path",
    "Config.TTL",
    "Config.Valid",
    "FS",
    "LockNotHeld",
    "Locker",
    "Locker.Acquire",
    "NewMemory",
    "Policy",
    "Policy.String",
  ]);
});

test("the ⌘K entries are the page's symbols and its aliases' methods, never a field", () => {
  const pages = apiPages(fixtureDocuments(), "v1");
  const entries = symbolEntries(pages.get("lock"), {
    release: "local",
    major: "v1",
    sourceBase: "https://github.com/o/r/blob/main",
  });
  assert.deepEqual(
    entries.map((e) => [e.kind, e.qualified, e.url, e.via ?? null]),
    [
      ["var", "lock.LockNotHeld", "/local/v1/lock/#LockNotHeld", null],
      ["func", "lock.NewMemory", "/local/v1/lock/#NewMemory", null],
      ["alias", "lock.Config", "/local/v1/lock/#Config", null],
      [
        "method",
        "lock.Config.Valid",
        "/local/v1/lock/#Config.Valid",
        `go:${P}/lock.Config`,
      ],
      ["alias", "lock.FS", "/local/v1/lock/#FS", null],
      ["alias", "lock.Locker", "/local/v1/lock/#Locker", null],
      [
        "method",
        "lock.Locker.Acquire",
        "/local/v1/lock/#Locker.Acquire",
        `go:${P}/lock.Locker`,
      ],
      ["type", "lock.Policy", "/local/v1/lock/#Policy", null],
      ["method", "lock.Policy.String", "/local/v1/lock/#Policy.String", null],
    ],
  );
  const newMemory = entries[1];
  assert.equal(newMemory.doc, "NewMemory returns a Locker.");
  assert.equal(
    newMemory.signature,
    "func NewMemory(cfg Config) (Locker, error)",
  );
  assert.equal(newMemory.sourceUrl, "https://github.com/o/r/blob/main/x.go");
  assert.equal(entries[0].deprecated, true);
  assert.equal(entries[6].id, `go:${CORE}.Locker.Acquire`);
  assert.equal(entries[6].receiver, "Locker");
});

test("a page path is the package's under pkg/<major>, internal and the root left out", () => {
  assert.equal(pageOf(`${P}/data/codec`, P), "data/codec");
  assert.equal(pageOf(P, P), null);
  assert.equal(pageOf(`${P}/lock/internal/impl`, P), null);
  assert.equal(pageOf(`${ROOT}/internal/core/lock`, P), null);
  assert.equal(pageOf(`${P}x/lock`, P), null);
});

test("a tree without docs/api has no API, and another format is refused by name", async () => {
  const root = await mkdtemp(join(tmpdir(), "api-test-"));
  try {
    assert.equal(await readApi(root), null);
    await mkdir(join(root, "docs", "api", "sdk"), { recursive: true });
    await writeFile(join(root, "docs", "api", "schema.json"), "{}");
    await assert.rejects(readApi(root), /no module document/);
    const [doc, vendor] = fixtureDocuments();
    await writeFile(join(root, "docs", "api", "sdk.json"), JSON.stringify(doc));
    await writeFile(
      join(root, "docs", "api", "sdk", "aws.json"),
      JSON.stringify(vendor),
    );
    const read = await readApi(root);
    assert.deepEqual(
      read.map((d) => d.module),
      [ROOT, `${ROOT}/third-party/aws`],
    );
    await writeFile(
      join(root, "docs", "api", "sdk.json"),
      JSON.stringify({ ...doc, format: "sdk.api/v2" }),
    );
    await assert.rejects(readApi(root), /"sdk\.api\/v2" is not sdk\.api\/v1/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

// ─── The repository's own docs/api ─────────────────────────────────

test("the repository's docs/api gives every pkg/v1 page its API, and every alias its owner's members", async (t) => {
  if (!existsSync(join(REPO_ROOT, "docs", "api", "schema.json"))) {
    t.skip("no docs/api in this checkout");
    return;
  }
  const documents = await readApi(REPO_ROOT);
  const pages = apiPages(documents, "v1");
  //: every page the portal publishes under pkg/v1 has an API, and the other
  //: way round: a package docs/api records and no README publishes would be
  //: a ⌘K entry linking nowhere.
  assert.deepEqual(
    [...pages.keys()],
    await listPackageDirs(join(REPO_ROOT, "pkg", "v1")),
  );

  //: counted apart, from the records: every distinct id under pkg/v1, and per
  //: alias the methods its owner declares.
  const all = documents.flatMap((d) => d.symbols);
  const inScope = all.filter(
    (s) => pageOf(s.package, `${documents[0].module}/pkg/v1`) !== null,
  );
  const methods = new Map();
  for (const s of all) {
    if (s.kind !== "method") continue;
    const owner = `go:${s.package}.${s.recv}`;
    if (!methods.has(owner)) methods.set(owner, new Set());
    methods.get(owner).add(s.id);
  }
  let members = 0;
  for (const s of new Map(inScope.map((x) => [x.id, x])).values()) {
    if (s.kind === "alias" && s.owner)
      members += methods.get(s.owner)?.size ?? 0;
  }
  const entries = [...pages.values()].flatMap((m) =>
    symbolEntries(m, { release: "local", major: "v1" }),
  );
  assert.equal(
    entries.filter((e) => !e.via).length,
    new Set(inScope.map((s) => s.id)).size,
  );
  assert.equal(entries.filter((e) => e.via).length, members);
  assert.equal(
    new Set(entries.map((e) => e.url)).size,
    entries.length,
    "two entries share an anchor",
  );
});
