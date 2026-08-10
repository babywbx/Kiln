import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const css = await readFile("modules/httpserver/admin/assets/app.css", "utf8");
const kit = await readFile("modules/httpserver/admin/assets/ui/kit.js", "utf8");

test("card actions wrap without squeezing button labels outside their bounds", () => {
  assert.match(kit, /action \? h\("div", \{ class: "card-action" \}, action\) : null/);
  assert.match(css, /\.card-head\s*\{[^}]*flex-wrap:\s*wrap/);
  assert.match(css, /\.card-title\s*\{[^}]*flex:\s*1 1 auto[^}]*overflow-wrap:\s*anywhere/);
  assert.match(css, /\.card-action\s*\{[^}]*flex:\s*0 0 auto/);
  assert.match(css, /\.button > span\s*\{[^}]*overflow:\s*hidden[^}]*text-overflow:\s*ellipsis/);
});

test("icon-only buttons remain circular at desktop and compact breakpoints", () => {
  assert.match(css, /\.icon-button\s*\{[^}]*aspect-ratio:\s*1/);
  assert.match(css, /\.icon-button\s*\{\s*width:\s*42px;\s*height:\s*42px;\s*min-width:\s*42px;\s*min-height:\s*42px;/);
});

test("modal content and actions cannot force horizontal overflow", () => {
  assert.match(css, /\.modal-title\s*\{[^}]*overflow-wrap:\s*anywhere/);
  assert.match(css, /\.modal-actions\s*\{[^}]*flex-wrap:\s*wrap/);
  assert.match(css, /\.notice\s*\{[^}]*overflow-wrap:\s*anywhere/);
});
