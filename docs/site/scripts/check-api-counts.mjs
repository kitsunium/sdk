#!/usr/bin/env node
// docs/site/scripts/check-api-counts.mjs — the ⌘K index and the API sections
// against docs/api, counted another way.
//
// docs/api is the SDK's exported API read from the code (tools/genindex
// -write-api, held by `make api-check`). The portal projects it twice through
// scripts/lib/api.mjs: the ⌘K symbol index and the API section of every
// package page. This script reads docs/api with code of its own — no import
// of lib/api.mjs — and fails unless, for one release and major:
//
//   - the ⌘K entries declared by a package of pkg/<major>/ are exactly the
//     distinct exported symbols docs/api records for those packages, once
//     each;
//   - the entries reached through an alias are exactly, per alias of those
//     packages, the methods docs/api records for the type the alias names
//     (its `owner`) — the alias members tools/genindex's census counts;
//   - each entry links to its package's page, at the anchor of its record;
//   - with dist/ built, each package page carries an element for every one
//     of those anchors, and for every field an alias's owner declares.
//
//   node scripts/check-api-counts.mjs [--release local] [--major v1]
//                                     [--repo ../..] [--dist dist]
//
// It reads the index dist/ serves when dist/ exists, else the one the
// prebuild wrote in public/_search/. Exit 0 when everything agrees; 1 with
// what differs, named, otherwise.

import { readdir, readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SITE_ROOT = resolve(__dirname, "..");

function arg(name, fallback) {
  const i = process.argv.indexOf(name);
  return i > 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
}

const RELEASE = arg("--release", "local");
const MAJOR = arg("--major", "v1");
const REPO = resolve(SITE_ROOT, arg("--repo", join("..", "..")));
const DIST = resolve(SITE_ROOT, arg("--dist", "dist"));

/** Every module document of docs/api. */
async function readDocuments(dir) {
  const out = [];
  async function walk(d) {
    for (const e of await readdir(d, { withFileTypes: true })) {
      const p = join(d, e.name);
      if (e.isDirectory()) await walk(p);
      else if (e.name.endsWith(".json") && e.name !== "schema.json")
        out.push(JSON.parse(await readFile(p, "utf8")));
    }
  }
  await walk(dir);
  return out;
}

/** A go: id's type prefix: the dots of the path's last element are %2e. */
function ownerKey(path, recv) {
  const slash = path.lastIndexOf("/");
  return `go:${path.slice(0, slash + 1)}${path.slice(slash + 1).replaceAll(".", "%2e")}.${recv}`;
}

const failures = [];
function differ(what, missing, extra) {
  if (missing.length === 0 && extra.length === 0) return;
  failures.push(
    `${what}: ${missing.length} missing, ${extra.length} extra` +
      missing
        .slice(0, 15)
        .map((x) => `\n    missing ${x}`)
        .join("") +
      extra
        .slice(0, 15)
        .map((x) => `\n    extra   ${x}`)
        .join(""),
  );
}
const minus = (a, b) => [...a].filter((x) => !b.has(x)).sort();

async function main() {
  const apiDir = join(REPO, "docs", "api");
  if (!existsSync(join(apiDir, "schema.json"))) {
    console.error(`[check-api-counts] ${apiDir} has no docs/api`);
    process.exit(1);
  }
  const documents = await readDocuments(apiDir);
  const root = documents.find((d) => d.dir === ".");
  if (!root) throw new Error("docs/api: no document of the root module");
  const prefix = `${root.module}/pkg/${MAJOR}/`;
  //: a page of the portal: a package below pkg/<major>/, none of its path
  //: elements internal, testdata, or hidden (lib/packages.mjs's rule).
  const pageOf = (path) => {
    if (!path.startsWith(prefix)) return null;
    const rel = path.slice(prefix.length);
    return rel
      .split("/")
      .some((e) => e === "internal" || e === "testdata" || /^[._]/.test(e))
      ? null
      : rel;
  };

  const all = documents.flatMap((d) => d.symbols);
  const methodsOf = new Map();
  const fieldsOf = new Map();
  for (const s of all) {
    if (s.kind === "method") {
      const k = ownerKey(s.package, s.recv);
      if (!methodsOf.has(k)) methodsOf.set(k, new Map());
      methodsOf.get(k).set(s.id, s.name);
    }
    if (s.kind === "type" && s.fields) {
      if (!fieldsOf.has(s.id)) fieldsOf.set(s.id, new Set());
      for (const f of s.fields) fieldsOf.get(s.id).add(f.name);
    }
  }

  //: what docs/api says the portal must show, keyed the way a page anchors it
  const declared = new Map(); // id → expected url
  const members = new Map(); // "<alias id> <method id>" → expected url
  const anchorsByPage = new Map(); // rel → Set of anchors
  const kinds = {};
  let aliases = 0;
  let memberFields = 0;
  const addAnchor = (rel, a) => {
    if (!anchorsByPage.has(rel)) anchorsByPage.set(rel, new Set());
    anchorsByPage.get(rel).add(a);
  };
  for (const p of root.packages) {
    const rel = pageOf(p.path);
    if (rel !== null && !anchorsByPage.has(rel))
      anchorsByPage.set(rel, new Set());
  }
  for (const s of all) {
    const rel = pageOf(s.package);
    if (rel === null || declared.has(s.id)) continue;
    const anchor = s.kind === "method" ? `${s.recv}.${s.name}` : s.name;
    declared.set(s.id, `/${RELEASE}/${MAJOR}/${rel}/#${anchor}`);
    kinds[s.kind] = (kinds[s.kind] ?? 0) + 1;
    addAnchor(rel, anchor);
    for (const f of s.kind === "type" ? (fieldsOf.get(s.id) ?? []) : [])
      addAnchor(rel, `${s.name}.${f}`);
    if (s.kind !== "alias" || !s.owner) continue;
    aliases++;
    for (const [mid, mname] of methodsOf.get(s.owner) ?? []) {
      members.set(
        `${s.id} ${mid}`,
        `/${RELEASE}/${MAJOR}/${rel}/#${s.name}.${mname}`,
      );
      addAnchor(rel, `${s.name}.${mname}`);
    }
    for (const f of fieldsOf.get(s.owner) ?? []) {
      memberFields++;
      addAnchor(rel, `${s.name}.${f}`);
    }
  }

  //: the index the site ships
  const name = `symbols-${RELEASE}-${MAJOR}.json`;
  const indexFile = existsSync(join(DIST, "_search", name))
    ? join(DIST, "_search", name)
    : join(SITE_ROOT, "public", "_search", name);
  if (!existsSync(indexFile)) {
    console.error(
      `[check-api-counts] no ⌘K index ${name} — run npm run build (or npm run prebuild)`,
    );
    process.exit(1);
  }
  const index = JSON.parse(await readFile(indexFile, "utf8"));
  if (index.schema !== 2 || index.source !== "docs/api") {
    failures.push(
      `${indexFile}: schema ${index.schema}, source ${index.source} — not an index written from docs/api`,
    );
  }
  const gotDeclared = new Map();
  const gotMembers = new Map();
  for (const e of index.symbols ?? []) {
    const target = e.via ? gotMembers : gotDeclared;
    const key = e.via ? `${e.via} ${e.id}` : e.id;
    if (target.has(key)) failures.push(`⌘K lists ${key} twice`);
    target.set(key, e.url);
  }
  differ(
    "⌘K entries declared by a package of pkg/" + MAJOR,
    minus(declared.keys(), new Set(gotDeclared.keys())),
    minus(gotDeclared.keys(), new Set(declared.keys())),
  );
  differ(
    "⌘K entries reached through an alias",
    minus(members.keys(), new Set(gotMembers.keys())),
    minus(gotMembers.keys(), new Set(members.keys())),
  );
  const misdirected = [];
  for (const [k, url] of [...declared, ...members]) {
    const got = gotDeclared.get(k) ?? gotMembers.get(k);
    if (got !== undefined && got !== url)
      misdirected.push(`${k}: ${got}, want ${url}`);
  }
  differ("⌘K entries linking elsewhere than their record", misdirected, []);

  //: the pages, when built
  let pagesChecked = 0;
  let anchorsChecked = 0;
  if (existsSync(join(DIST, RELEASE, MAJOR))) {
    const missing = [];
    for (const [rel, anchors] of anchorsByPage) {
      const page = join(DIST, RELEASE, MAJOR, rel, "index.html");
      if (!existsSync(page)) {
        missing.push(`${rel}: no page`);
        continue;
      }
      const html = await readFile(page, "utf8");
      const ids = new Set(
        [...html.matchAll(/\sid="([^"]*)"/g)].map((m) => m[1]),
      );
      if (!ids.has("api")) missing.push(`${rel}: no API section`);
      for (const a of anchors) {
        anchorsChecked++;
        if (!ids.has(a)) missing.push(`${rel}#${a}`);
      }
      pagesChecked++;
    }
    differ(`anchors on the ${RELEASE}/${MAJOR} package pages`, missing, []);
  }

  const total = new Set(all.map((s) => s.id)).size;
  console.log(
    `[check-api-counts] docs/api: ${documents.length} module documents, ${total} distinct exported symbols`,
  );
  console.log(
    `[check-api-counts] pkg/${MAJOR}: ${anchorsByPage.size} packages, ${declared.size} symbols (` +
      Object.entries(kinds)
        .sort()
        .map(([k, n]) => `${k} ${n}`)
        .join(", ") +
      `); ${aliases} aliases reach ${members.size} methods and ${memberFields} fields at their owners`,
  );
  console.log(
    `[check-api-counts] ⌘K ${RELEASE}/${MAJOR} (${indexFile.startsWith(DIST) ? "dist" : "public"}): ` +
      `${(index.symbols ?? []).length} entries = ${gotDeclared.size} declared + ${gotMembers.size} through an alias`,
  );
  if (pagesChecked > 0) {
    console.log(
      `[check-api-counts] pages: ${pagesChecked} API sections, ${anchorsChecked} anchors (symbols, alias methods and fields)`,
    );
  } else {
    console.log(
      `[check-api-counts] pages: dist/${RELEASE}/${MAJOR} not built — the index alone was checked`,
    );
  }
  if (failures.length > 0) {
    console.error(`\n[check-api-counts] FAILED:\n  ${failures.join("\n  ")}`);
    process.exit(1);
  }
  console.log(
    "[check-api-counts] the ⌘K index and the API sections equal docs/api",
  );
}

main().catch((err) => {
  console.error("[check-api-counts] FAILED:", err);
  process.exit(1);
});
