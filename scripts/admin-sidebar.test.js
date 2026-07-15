import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const css = await readFile("modules/httpserver/admin/assets/app.css", "utf8");

test("compact sidebar removes the status chip without recentering icon rows", () => {
  const navItem = css.match(/\.nav-item\s*\{([^}]*)\}/);
  assert.ok(navItem, "expected a navigation item rule");
  assert.match(navItem[1], /\bheight:\s*38px/);
  assert.match(css, /\.is-compact\s+\.instance\s*\{\s*display:\s*none;\s*\}/);
  assert.doesNotMatch(css, /\.is-compact\s+\.(?:sidebar-head|nav-item|sidebar-toggle)[^{]*\{[^}]*justify-content:\s*center/);
  assert.doesNotMatch(css, /\.is-compact\s+\.(?:nav-item|sidebar-toggle)[^{]*\{[^}]*padding-inline:\s*0/);
});
