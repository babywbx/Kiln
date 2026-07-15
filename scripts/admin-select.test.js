import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const css = await readFile("modules/httpserver/admin/assets/app.css", "utf8");

test("select hover preserves the chevron background layer", () => {
  const match = css.match(/input:not\(\[type="checkbox"\]\):hover,\s*select:hover,\s*textarea:hover\s*\{([^}]*)\}/);
  assert.ok(match, "expected a shared hover rule for form controls");
  assert.match(match[1], /\bbackground-color\s*:/);
  assert.doesNotMatch(match[1], /(^|;)\s*background\s*:/);
});

test("supported browsers receive the Luma select picker treatment", () => {
  assert.match(css, /@supports\s*\(appearance:\s*base-select\)/);
  assert.match(css, /::picker\(select\)/);
  assert.match(css, /select::picker-icon/);
  assert.match(css, /option::checkmark/);
});
