// Unit tests for an ADR's relative links on the portal (lib/adr.mjs).
// Run via `npm test` (node --test scripts/lib/*.test.mjs).

import { test } from "node:test";
import assert from "node:assert/strict";

import { rewriteAdrLinks } from "./adr.mjs";

const blob = "https://github.com/kitsunium/sdk/blob/v0.18.0";
const adrs = ["0005-sdk-error-codes", "0162-one-module"];

test("a link to another ADR points at its page, every spelling of it", () => {
  assert.equal(
    rewriteAdrLinks(
      "See [0162](0162-one-module.md), [codes](./0005-sdk-error-codes.md#registry) and [again](../adr/0162-one-module.md).",
      adrs,
      blob,
    ),
    "See [0162](../0162-one-module/), [codes](../0005-sdk-error-codes/#registry) and [again](../0162-one-module/).",
  );
});

test("another Markdown file of the repository points at it on GitHub", () => {
  assert.equal(
    rewriteAdrLinks(
      "[heap](../../internal/kernel/collections/heap/CLAUDE.md) and [gone](0999-not-an-adr.md)",
      adrs,
      blob,
    ),
    `[heap](${blob}/internal/kernel/collections/heap/CLAUDE.md) and [gone](${blob}/docs/adr/0999-not-an-adr.md)`,
  );
});

test("absolute links, anchors, code and paths outside the repository stay", () => {
  const body = [
    "[site](https://go.dev/doc/comment) [here](#context) [api](<#Delivery>)",
    "[out](../../../elsewhere.md) [script](../../scripts/x.sh)",
    "```go",
    "m := Map[K](0162-one-module.md)",
    "```",
    "after [0005](0005-sdk-error-codes.md)",
  ].join("\n");
  assert.equal(
    rewriteAdrLinks(body, adrs, blob),
    body.replace(
      "after [0005](0005-sdk-error-codes.md)",
      "after [0005](../0005-sdk-error-codes/)",
    ),
  );
});
