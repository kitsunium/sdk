// docs/site/scripts/lib/api.mjs — docs/api read for the docs portal.
//
// docs/api/<module>.json is the SDK's exported API read from the CODE by
// tools/genindex -write-api (`make api`, checked by `make api-check`): every
// exported symbol of every module of the workspace, internal packages
// included, on every cell of scripts/ci/platforms.sh, with its signature as
// its file spells it, its doc text, its cells and its file. docs/api/schema.json
// is the format. The portal reads it twice, through this module alone:
//
//   - each package page of pkg/<major>/ gets an API section (apiPages, then
//     ApiSection.astro), where an alias shows the fields and methods of the
//     type it names — the members go/doc, gomarkdoc and the READMEs cannot
//     show, because go/doc does not follow an alias;
//   - the ⌘K symbol index (symbolEntries, written by gen-symbols.mjs) is the
//     same records: every exported symbol of the page's package, and every
//     method an alias reaches at its owner.
//
// A release cut before docs/api existed has none: readApi answers null and the
// release keeps its README pages (sync-versions.mjs).
//
// Doc text is data, never markup: docBlocks splits it the way go/doc reads a
// comment (paragraphs, headings, code, lists) and inlineSegments cuts a block
// into text and links, so a component renders text nodes and builds no HTML
// from a string.

import { readdir, readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { join } from "node:path";

/** The docs/api format this reader understands — schema.json's `format`. */
export const API_FORMAT = "sdk.api/v1";

/** The page model's own shape version, written into every model. */
export const MODEL_FORMAT = 1;

/** The four sections of an API section, in the order a reader scans them. */
export const SECTION_ORDER = Object.freeze([
  { kind: "const", title: "Constants" },
  { kind: "var", title: "Variables" },
  { kind: "func", title: "Functions" },
  { kind: "type", title: "Types" },
]);

/** Directory names under pkg/<major>/ that hold no page (lib/packages.mjs). */
const UNPUBLISHED = new Set(["internal", "testdata"]);

// ─── Reading ───────────────────────────────────────────────────────

/**
 * Every module document under `<root>/docs/api`, sorted by file name, or null
 * when the tree has no docs/api (no `schema.json`): a release cut before
 * tools/genindex wrote it. A document in another format is refused by name —
 * a portal that half-reads an API would show a reference missing what it could
 * not read, and nothing would look broken.
 *
 * @param {string} root — a checkout: the repository, or a release's worktree
 * @returns {Promise<object[] | null>}
 */
export async function readApi(root) {
  const dir = join(root, "docs", "api");
  if (!existsSync(join(dir, "schema.json"))) return null;
  /** @type {string[]} */
  const files = [];
  /** @param {string} d */
  async function walk(d) {
    for (const e of await readdir(d, { withFileTypes: true })) {
      const p = join(d, e.name);
      if (e.isDirectory()) await walk(p);
      else if (e.name.endsWith(".json") && p !== join(dir, "schema.json"))
        files.push(p);
    }
  }
  await walk(dir);
  files.sort();
  const documents = [];
  for (const f of files) {
    const doc = JSON.parse(await readFile(f, "utf8"));
    if (doc?.format !== API_FORMAT) {
      throw new Error(
        `${f}: docs/api format ${JSON.stringify(doc?.format)} is not ${API_FORMAT}, the one this portal reads`,
      );
    }
    documents.push(doc);
  }
  if (documents.length === 0) throw new Error(`${dir}: no module document`);
  return documents;
}

// ─── go: ids ───────────────────────────────────────────────────────

/**
 * Splits a go: id into its import path and the name after it: the path ends
 * at the first dot after its last slash, a dot of its last element being
 * written %2e (schema.json, goid).
 *
 * @param {string} id — "go:net/http.(*Client).Do"
 * @returns {{ path: string, rest: string }}
 */
export function splitGoID(id) {
  const body = id.startsWith("go:") ? id.slice(3) : id;
  const slash = body.lastIndexOf("/");
  const dot = body.indexOf(".", slash + 1);
  if (dot < 0) return { path: body.replaceAll("%2e", "."), rest: "" };
  return {
    path: body.slice(0, dot).replaceAll("%2e", "."),
    rest: body.slice(dot + 1),
  };
}

/**
 * The id prefix of a package: its path with the dots of its last element
 * written %2e, as the linker escapes them.
 *
 * @param {string} path
 * @returns {string}
 */
export function escapePath(path) {
  const slash = path.lastIndexOf("/");
  return (
    path.slice(0, slash + 1) + path.slice(slash + 1).replaceAll(".", "%2e")
  );
}

/**
 * The id of a named type: go:<escaped path>.<Name>.
 *
 * @param {string} path
 * @param {string} name
 * @returns {string}
 */
export function typeID(path, name) {
  return `go:${escapePath(path)}.${name}`;
}

// ─── Declarations ──────────────────────────────────────────────────

/**
 * Splits `s` at every top-level `sep`: one inside parentheses, brackets,
 * braces or a quoted string does not split.
 *
 * @param {string} s
 * @param {string} sep — one character
 * @returns {string[]}
 */
export function splitTopLevel(s, sep) {
  const out = [];
  let depth = 0;
  let quote = "";
  let start = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (quote) {
      if (c === "\\" && quote === '"') i++;
      else if (c === quote) quote = "";
      continue;
    }
    if (c === '"' || c === "`") quote = c;
    else if (c === "(" || c === "[" || c === "{") depth++;
    else if (c === ")" || c === "]" || c === "}") depth--;
    else if (c === sep && depth === 0) {
      out.push(s.slice(start, i));
      start = i + 1;
    }
  }
  out.push(s.slice(start));
  return out.map((x) => x.trim()).filter((x) => x !== "");
}

/**
 * Splits a leading type parameter list off a signature: "[K comparable, V
 * any] struct{…}" gives "[K comparable, V any]" and "struct{…}".
 *
 * @param {string} sig
 * @returns {{ params: string, rest: string }}
 */
export function splitTypeParams(sig) {
  if (!sig.startsWith("[")) return { params: "", rest: sig };
  let depth = 0;
  for (let i = 0; i < sig.length; i++) {
    if (sig[i] === "[") depth++;
    else if (sig[i] === "]" && --depth === 0) {
      return { params: sig.slice(0, i + 1), rest: sig.slice(i + 1).trim() };
    }
  }
  return { params: "", rest: sig };
}

/**
 * A struct's or an interface's body, from its one-line spelling, as the lines
 * gofmt would write between its braces: fields aligned, a tag between
 * backquotes when it holds none.
 *
 * @param {string} keyword — "struct" or "interface"
 * @param {string} body — what the braces hold
 * @param {Array<{name: string, type: string, tag?: string, embedded?: boolean}>} [fields]
 * @returns {string[]}
 */
function bodyLines(keyword, body, fields) {
  if (keyword === "interface") return splitTopLevel(body, ";");
  if (!fields || fields.length === 0) return [];
  const named = fields.filter((f) => !f.embedded);
  const width = Math.max(0, ...named.map((f) => f.name.length));
  const typeWidth = Math.max(
    0,
    ...named.filter((f) => f.tag).map((f) => f.type.length),
  );
  return fields.map((f) => {
    if (f.embedded) return f.type + (f.tag ? ` ${quoteTag(f.tag)}` : "");
    const head = `${f.name.padEnd(width)} ${f.tag ? f.type.padEnd(typeWidth) : f.type}`;
    return f.tag ? `${head} ${quoteTag(f.tag)}` : head;
  });
}

/** @param {string} tag */
function quoteTag(tag) {
  return tag.includes("`") ? JSON.stringify(tag) : `\`${tag}\``;
}

/**
 * The declaration a reader expects for a type: `type Name[P] <underlying>`,
 * a struct or an interface written over several lines.
 *
 * @param {string} name
 * @param {string} spelled — the docs/api spelling: "[P] <underlying>"
 * @param {Array<object>} [fields] — a struct's exported fields
 * @returns {string}
 */
export function typeDecl(name, spelled, fields) {
  const { params, rest } = splitTypeParams(spelled);
  const m = rest.match(/^(struct|interface)\{([\s\S]*)\}$/);
  if (!m) return `type ${name}${params} ${rest}`;
  const lines = bodyLines(m[1], m[2], fields);
  if (lines.length === 0) {
    return m[1] === "struct" && m[2].trim() === ""
      ? `type ${name}${params} struct {\n\t// no exported field\n}`
      : `type ${name}${params} ${m[1]}{}`;
  }
  return `type ${name}${params} ${m[1]} {\n${lines.map((l) => `\t${l}`).join("\n")}\n}`;
}

/**
 * One record's declaration, as gofmt would start it.
 *
 * @param {object} record — a docs/api symbol
 * @param {{ name?: string, recv?: string }} [as] — the name and receiver to
 *   write, when the record is shown under another one (an alias's member)
 * @returns {string}
 */
export function declOf(record, as = {}) {
  const name = as.name ?? record.name;
  const s = record.spelled;
  switch (record.kind) {
    case "func":
      return `func ${name}${s.slice("func".length)}`;
    case "method": {
      const recv = as.recv ?? record.recv;
      const star = isPointerMethod(record.id) ? "*" : "";
      return `func (${star}${recv}) ${name}${s.slice("func".length)}`;
    }
    case "type":
      return typeDecl(name, s, record.fields);
    case "alias": {
      const { params, rest } = splitTypeParams(s);
      return `type ${name}${params} = ${rest}`;
    }
    case "const":
      return s.startsWith("untyped ")
        ? `const ${name} = ${record.value}`
        : `const ${name} ${s} = ${record.value}`;
    case "var":
      return `var ${name} ${s}`;
    default:
      return `${record.kind} ${name}`;
  }
}

/**
 * The one-line form of a declaration, for a search result.
 *
 * @param {string} decl
 * @returns {string}
 */
export function oneLine(decl) {
  return decl
    .replace(/\s*\n\s*/g, "; ")
    .replace(/\{; /g, "{ ")
    .replace(/; \}/g, " }");
}

/**
 * Whether a method's id names a pointer receiver: go:<path>.(*T).M.
 *
 * @param {string} id
 * @returns {boolean}
 */
export function isPointerMethod(id) {
  return splitGoID(id).rest.startsWith("(*");
}

// ─── Doc text ──────────────────────────────────────────────────────

const LIST_MARKER = /^\s*(?:[-*+•]|\d+[.)])\s+/;

/**
 * Splits a doc comment's text (ast.CommentGroup.Text) into the blocks go/doc
 * reads in it: paragraphs (their lines joined), `# ` headings, code (an
 * indented span, dedented) and lists (an indented span whose first line opens
 * with a bullet or a number). Every block holds text only.
 *
 * @param {string | undefined} text
 * @returns {Array<{type: "p" | "h" | "code", text: string} | {type: "list", ordered: boolean, items: string[]}>}
 */
export function docBlocks(text) {
  const lines = (text ?? "").replace(/\r\n?/g, "\n").split("\n");
  /** @type {Array<any>} */
  const blocks = [];
  const blank = (l) => l === undefined || l.trim() === "";
  const indented = (l) => /^[ \t]/.test(l) && !blank(l);
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (blank(line)) {
      i++;
      continue;
    }
    if (indented(line)) {
      //: an indented span runs over indented and blank lines; a blank line
      //: then an unindented one ends it.
      let j = i;
      while (j < lines.length && (indented(lines[j]) || blank(lines[j]))) j++;
      let span = lines.slice(i, j);
      while (span.length > 0 && blank(span.at(-1))) span.pop();
      i = j;
      if (LIST_MARKER.test(span[0])) {
        const ordered = /^\s*\d/.test(span[0]);
        const items = [];
        for (const l of span) {
          if (blank(l)) continue;
          if (LIST_MARKER.test(l))
            items.push(l.replace(LIST_MARKER, "").trim());
          else if (items.length > 0) items[items.length - 1] += ` ${l.trim()}`;
        }
        blocks.push({ type: "list", ordered, items });
      } else {
        blocks.push({ type: "code", text: dedent(span).join("\n") });
      }
      continue;
    }
    if (
      /^# \S/.test(line) &&
      (i === 0 || blank(lines[i - 1])) &&
      blank(lines[i + 1])
    ) {
      blocks.push({ type: "h", text: line.slice(2).trim() });
      i++;
      continue;
    }
    const para = [];
    while (i < lines.length && !blank(lines[i]) && !indented(lines[i])) {
      para.push(lines[i].trim());
      i++;
    }
    blocks.push({ type: "p", text: para.join(" ") });
  }
  return blocks;
}

/** @param {string[]} span */
function dedent(span) {
  const widths = span
    .filter((l) => l.trim() !== "")
    .map((l) => l.match(/^[ \t]*/)[0]);
  let prefix = widths[0] ?? "";
  for (const w of widths) {
    while (!w.startsWith(prefix)) prefix = prefix.slice(0, -1);
  }
  return span.map((l) =>
    l.startsWith(prefix) ? l.slice(prefix.length) : l.trimStart(),
  );
}

const DOC_LINK =
  /\[(\*?(?:[A-Za-z0-9_.-]+\/)*[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*){0,2})\]/g;
const URL_TEXT = /https?:\/\/[^\s<>"'`]+/g;

/**
 * Cuts a block's text into text and links: a doc link `[Name]` or
 * `[Name.Member]` whose anchor `resolve` knows becomes a link to it, any other
 * doc link its text without brackets — go doc's own rendering —, and an http
 * or https URL a link to itself. A bracket that is no doc link (`m[key]`,
 * `[0, n)`) stays as written: go/doc wants one preceded and followed by a
 * space, a punctuation mark or a line's end.
 *
 * @param {string} text
 * @param {(target: string) => string | null} [resolve] — the anchor of a
 *   same-page target, or null
 * @returns {Array<{text: string, href?: string}>}
 */
export function inlineSegments(text, resolve = () => null) {
  /** @type {Array<{start: number, end: number, text: string, href?: string}>} */
  const marks = [];
  for (const m of text.matchAll(URL_TEXT)) {
    let url = m[0];
    while (/[.,;:!?)\]]$/.test(url)) url = url.slice(0, -1);
    marks.push({
      start: m.index,
      end: m.index + url.length,
      text: url,
      href: url,
    });
  }
  for (const m of text.matchAll(DOC_LINK)) {
    const start = m.index;
    const end = start + m[0].length;
    if (marks.some((x) => start < x.end && end > x.start)) continue;
    const before = start === 0 ? " " : text[start - 1];
    const after = end >= text.length ? " " : text[end];
    if (/[A-Za-z0-9_]/.test(before) || /[A-Za-z0-9_(:[]/.test(after)) continue;
    const target = m[1].replace(/^\*/, "");
    const anchor = target.includes("/") ? null : resolve(target);
    marks.push(
      anchor
        ? { start, end, text: m[1], href: `#${anchor}` }
        : { start, end, text: m[1] },
    );
  }
  marks.sort((a, b) => a.start - b.start);
  const out = [];
  let at = 0;
  for (const mk of marks) {
    if (mk.start > at) out.push({ text: text.slice(at, mk.start) });
    out.push(mk.href ? { text: mk.text, href: mk.href } : { text: mk.text });
    at = mk.end;
  }
  if (at < text.length) out.push({ text: text.slice(at) });
  //: adjacent plain segments merged, so a renderer writes one text node.
  return out.reduce((acc, seg) => {
    const last = acc.at(-1);
    if (last && !last.href && !seg.href) last.text += seg.text;
    else acc.push({ ...seg });
    return acc;
  }, []);
}

/**
 * go/doc's synopsis: the first sentence of the doc's first block when that
 * block is a paragraph — a period then a space ends it, unless the period
 * follows exactly one capital letter (`U.S. law`) — with its doc links as
 * their text.
 *
 * @param {string | undefined} text
 * @returns {string}
 */
export function synopsis(text) {
  const first = docBlocks(text)[0];
  if (!first || first.type !== "p") return "";
  const s = inlineSegments(first.text)
    .map((x) => x.text)
    .join("");
  let ppp = "";
  let pp = "";
  let p = "";
  for (let i = 0; i < s.length; i++) {
    const q = s[i];
    const upper = (c) =>
      c !== "" && c === c.toUpperCase() && c !== c.toLowerCase();
    if (q === " " && p === "." && (!upper(pp) || upper(ppp)))
      return s.slice(0, i);
    ppp = pp;
    pp = p;
    p = q;
  }
  return s;
}

/**
 * Whether a doc marks its symbol deprecated: one of its paragraphs opens with
 * "Deprecated: ", go/doc's convention.
 *
 * @param {string | undefined} text
 * @returns {boolean}
 */
export function isDeprecated(text) {
  return docBlocks(text).some(
    (b) => b.type === "p" && b.text.startsWith("Deprecated: "),
  );
}

// ─── Pages ─────────────────────────────────────────────────────────

/**
 * The records of every document, grouped once for the projections below.
 *
 * @param {object[]} documents
 */
function indexDocuments(documents) {
  const root = documents.find((d) => d.dir === ".");
  if (!root)
    throw new Error('docs/api: no document of the root module (dir ".")');
  /** @type {Map<string, object[]>} records by id, in document order */
  const byID = new Map();
  /** @type {Map<string, string[]>} method ids by their type's id */
  const methodsOf = new Map();
  /** @type {Map<string, {doc: object, pkg: object}>} package records by path */
  const packages = new Map();
  /** @type {Map<string, object>} each record's module document */
  const docOf = new Map();
  for (const doc of documents) {
    for (const p of doc.packages) {
      if (!packages.has(p.path)) packages.set(p.path, { doc, pkg: p });
    }
    for (const s of doc.symbols) {
      if (!byID.has(s.id)) {
        byID.set(s.id, []);
        docOf.set(s.id, doc);
        if (s.kind === "method") {
          const owner = typeID(s.package, s.recv);
          if (!methodsOf.has(owner)) methodsOf.set(owner, []);
          methodsOf.get(owner).push(s.id);
        }
      }
      byID.get(s.id).push(s);
    }
  }
  return { root, byID, methodsOf, packages, docOf };
}

/**
 * A record's file, relative to the repository: docs/api writes it relative to
 * its module, whose directory the document names.
 *
 * @param {object} doc
 * @param {string} file
 */
function repoFile(doc, file) {
  return doc.dir === "." ? file : `${doc.dir}/${file}`;
}

/**
 * The forms of one symbol: one per record, each with the cells it holds on
 * (none when it holds on every cell).
 *
 * @param {object[]} records
 * @param {object} doc — their module document
 * @param {{ name?: string, recv?: string }} [as]
 */
function formsOf(records, doc, as) {
  return records.map((r) => {
    const form = { decl: declOf(r, as), file: repoFile(doc, r.file) };
    if (r.doc) form.doc = r.doc;
    if (r.platforms) form.platforms = r.platforms;
    if (r.value !== undefined) form.value = r.value;
    if (r.code) form.code = r.code;
    if (r.init) form.init = r.init;
    return form;
  });
}

/**
 * A struct type's exported fields across its forms: one entry per distinct
 * field, with the cells it holds on when some form lacks it.
 *
 * @param {object[]} records — the type's records
 * @param {string[]} cells — the document's cells
 * @param {string} prefix — the anchor prefix, "<Type>."
 */
function fieldsOf(records, cells, prefix) {
  /** @type {Map<string, object>} */
  const merged = new Map();
  for (const r of records) {
    for (const f of r.fields ?? []) {
      const key = `${f.name}\u0000${f.type}\u0000${f.tag ?? ""}\u0000${f.embedded ? 1 : 0}`;
      if (!merged.has(key)) {
        const field = {
          anchor: `${prefix}${f.name}`,
          name: f.name,
          type: f.type,
        };
        if (f.embedded) field.embedded = true;
        if (f.tag) field.tag = f.tag;
        if (f.doc) field.doc = f.doc;
        merged.set(key, { field, on: new Set() });
      }
      for (const c of r.platforms ?? cells) merged.get(key).on.add(c);
    }
  }
  return [...merged.values()].map(({ field, on }) =>
    on.size === cells.length
      ? field
      : { ...field, platforms: cells.filter((c) => on.has(c)) },
  );
}

/**
 * The methods a type declares, as page symbols: anchored `<as>.<Method>` and
 * written with `as` for receiver — the type's own name, or an alias's.
 *
 * @param {ReturnType<typeof indexDocuments>} ix
 * @param {string} owner — the type's id
 * @param {string} as — the name the page shows the type under
 */
function methodSymbols(ix, owner, as) {
  return (ix.methodsOf.get(owner) ?? []).map((id) => {
    const records = ix.byID.get(id);
    const r = records[0];
    return {
      anchor: `${as}.${r.name}`,
      id,
      kind: "method",
      name: r.name,
      recv: as,
      forms: formsOf(records, ix.docOf.get(id), { recv: as }),
    };
  });
}

/**
 * One page symbol: its forms, and for a type its fields and methods; for an
 * alias the type it names — its declaration, its fields and its methods,
 * shown under the alias's name — or, outside docs/api (the standard library),
 * that type's name alone.
 *
 * @param {ReturnType<typeof indexDocuments>} ix
 * @param {string} id
 */
function pageSymbol(ix, id) {
  const records = ix.byID.get(id);
  const r = records[0];
  const doc = ix.docOf.get(id);
  const sym = {
    anchor: r.name,
    id,
    kind: r.kind,
    name: r.name,
    forms: formsOf(records, doc),
  };
  if (r.kind === "type") {
    const fields = fieldsOf(records, doc.cells, `${r.name}.`);
    if (fields.length > 0) sym.fields = fields;
    const methods = methodSymbols(ix, id, r.name);
    if (methods.length > 0) sym.methods = methods;
  }
  if (r.kind === "alias" && r.owner) {
    const { path, rest } = splitGoID(r.owner);
    const owned = ix.byID.get(r.owner);
    if (!owned) {
      sym.owner = { id: r.owner, path, name: rest, external: true };
    } else {
      const ownerDoc = ix.docOf.get(r.owner);
      sym.owner = {
        id: r.owner,
        path,
        name: rest,
        forms: formsOf(owned, ownerDoc),
      };
      const fields = fieldsOf(owned, ownerDoc.cells, `${r.name}.`);
      if (fields.length > 0) sym.fields = fields;
      const methods = methodSymbols(ix, r.owner, r.name);
      if (methods.length > 0) sym.methods = methods;
    }
  }
  return sym;
}

/**
 * The page path of a package under pkg/<major>/, or null when it has no page:
 * the directory itself, and anything under an internal or testdata element.
 *
 * @param {string} path — an import path
 * @param {string} prefix — "<root module>/pkg/<major>"
 * @returns {string | null}
 */
export function pageOf(path, prefix) {
  if (!path.startsWith(`${prefix}/`)) return null;
  const rel = path.slice(prefix.length + 1);
  if (
    rel
      .split("/")
      .some((e) => UNPUBLISHED.has(e) || e.startsWith(".") || e.startsWith("_"))
  )
    return null;
  return rel;
}

/**
 * One page model per package of pkg/<major>/ that docs/api records, keyed by
 * the package's path under pkg/<major>/ — the path its page sits at
 * (lib/packages.mjs). Every exported symbol of the package is in its model
 * once, with one form per record; an alias carries its owner's fields and
 * methods.
 *
 * @param {object[]} documents — readApi's answer
 * @param {string} major — "v1"
 * @returns {Map<string, object>}
 */
export function apiPages(documents, major) {
  const ix = indexDocuments(documents);
  const prefix = `${ix.root.module}/pkg/${major}`;
  /** @type {Map<string, object>} */
  const pages = new Map();
  for (const [path, { doc, pkg }] of ix.packages) {
    const rel = pageOf(path, prefix);
    if (rel === null) continue;
    pages.set(rel, {
      format: MODEL_FORMAT,
      module: doc.module,
      cells: doc.cells,
      package: { path, name: pkg.name, rel, dir: repoFile(doc, pkg.dir) },
      sections: SECTION_ORDER.map(({ kind, title }) => ({
        kind,
        title,
        symbols: [],
      })),
    });
  }
  const section = { const: 0, var: 1, func: 2, type: 3, alias: 3 };
  for (const [id, records] of ix.byID) {
    const r = records[0];
    if (r.kind === "method") continue;
    const rel = pageOf(r.package, prefix);
    if (rel === null || !pages.has(rel)) continue;
    pages.get(rel).sections[section[r.kind]].symbols.push(pageSymbol(ix, id));
  }
  return new Map([...pages].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
}

/**
 * Every anchor a page model gives an element: each symbol's, each member's.
 *
 * @param {object} model
 * @returns {Set<string>}
 */
export function pageAnchors(model) {
  const out = new Set();
  for (const s of model.sections) {
    for (const sym of s.symbols) {
      out.add(sym.anchor);
      for (const f of sym.fields ?? []) out.add(f.anchor);
      for (const m of sym.methods ?? []) out.add(m.anchor);
    }
  }
  return out;
}

// ─── ⌘K ────────────────────────────────────────────────────────────

/**
 * The ⌘K entries of one page: every symbol of its package, and every method
 * of its types — an alias's included, which its owner declares and the page
 * shows under the alias. A field is shown on the page and is no entry. Each
 * entry's url is the page's, without the deploy base (the search client adds
 * it), anchored at the record the page renders.
 *
 * @param {object} model — one of apiPages' values
 * @param {{ release: string, major: string, sourceBase?: string }} at —
 *   sourceBase is "https://github.com/<owner>/<repo>/blob/<ref>", when known
 * @returns {object[]}
 */
export function symbolEntries(model, { release, major, sourceBase }) {
  const url = `/${release}/${major}/${model.package.rel}/`;
  const pkg = model.package;
  const entry = (sym, extra) => {
    const form = sym.forms[0];
    const doc = form.doc;
    const e = {
      kind: sym.kind,
      name: sym.name,
      qualified: `${pkg.name}.${sym.recv ? `${sym.recv}.` : ""}${sym.name}`,
      package: pkg.path,
      packageShort: pkg.rel,
      signature: oneLine(form.decl),
      url: `${url}#${sym.anchor}`,
      id: sym.id,
      ...extra,
    };
    const summary = synopsis(doc);
    if (summary) e.doc = summary;
    if (sym.recv) e.receiver = sym.recv;
    if (isDeprecated(doc)) e.deprecated = true;
    if (sourceBase) e.sourceUrl = `${sourceBase}/${form.file}`;
    return e;
  };
  const out = [];
  for (const s of model.sections) {
    for (const sym of s.symbols) {
      out.push(entry(sym));
      const via = sym.kind === "alias" ? { via: sym.id } : {};
      for (const m of sym.methods ?? []) out.push(entry(m, via));
    }
  }
  return out;
}

// ─── Source links ──────────────────────────────────────────────────

/**
 * The ref a release's source links point at: its tag, or — the working tree,
 * which has none — the branch build-info names, else its commit (the same
 * rule as EditLink.astro).
 *
 * @param {{ branch?: string | null, commitFull?: string | null } | null | undefined} build
 * @param {string | null | undefined} tag
 * @returns {string | null}
 */
export function sourceRef(build, tag) {
  if (tag) return tag;
  if (build?.branch && build.branch !== "HEAD") return build.branch;
  return build?.commitFull ?? null;
}

/**
 * "https://github.com/<owner>/<repo>/blob/<ref>", the prefix a record's
 * repository-relative file is appended to; null when either is unknown.
 *
 * @param {string | null | undefined} repoUrl
 * @param {string | null} ref
 * @returns {string | null}
 */
export function blobBase(repoUrl, ref) {
  return repoUrl && ref ? `${repoUrl.replace(/\/$/, "")}/blob/${ref}` : null;
}
