#!/usr/bin/env node
// docs/site/scripts/sync-content.mjs — Astro's content sync, waited for.
//
// The prebuild wipes Astro's content store (node_modules/.astro/, by
// sync-versions.mjs), so a build syncs every entry into a fresh store, written
// to disk by a debounced write. Astro 5.18 does not wait for that write when it
// is in flight as the sync ends — MutableDataStore#writeFileAtomic returns at
// once for a file it is writing, and waitUntilSaveComplete resolves — and
// `astro build` then reads the store before the write lands: the file is
// absent, every collection reads empty, and the build renders the 80 pages
// that need no collection out of 9,500, with exit status 0. A full build of
// every release here did exactly that. `astro sync` is no cure: the astro CLI
// calls process.exit as soon as its command resolves, which can kill the write
// before it lands.
//
// This script runs the same sync through Astro's JavaScript API and lets Node
// exit on its own — once nothing is pending, so once the write has landed —
// and fails if the store is still missing then. The `astro build` that follows
// loads the complete store and re-syncs only what changed, in seconds.

import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { sync } from "astro";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SITE_ROOT = resolve(__dirname, "..");
const STORE = join(SITE_ROOT, "node_modules", ".astro", "data-store.json");

process.on("exit", (code) => {
  if (code === 0 && !existsSync(STORE)) {
    console.error(
      `[sync-content] ${STORE} was not written: the build would render no collection`,
    );
    process.exitCode = 1;
  }
});

await sync({ root: SITE_ROOT });
