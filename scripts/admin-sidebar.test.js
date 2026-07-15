import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const css = await readFile("modules/httpserver/admin/assets/app.css", "utf8");
const app = await readFile("modules/httpserver/admin/assets/app.js", "utf8");

test("compact sidebar removes the status chip without recentering icon rows", () => {
  const navItem = css.match(/\.nav-item\s*\{([^}]*)\}/);
  assert.ok(navItem, "expected a navigation item rule");
  assert.match(navItem[1], /\bheight:\s*38px/);
  assert.match(css, /\.is-compact\s+\.instance\s*\{\s*display:\s*none;\s*\}/);
  assert.doesNotMatch(css, /\.is-compact\s+\.(?:sidebar-head|nav-item)[^{]*\{[^}]*justify-content:\s*center/);
  assert.doesNotMatch(css, /\.is-compact\s+\.(?:nav-item|sidebar-toggle)[^{]*\{[^}]*padding-inline:\s*0/);
});

test("compact sidebar keeps a stable icon inside a compact toggle surface", () => {
  const compactToggle = css.match(/\.is-compact\s+\.sidebar-toggle\s*\{([^}]*)\}/);
  assert.ok(compactToggle, "expected compact toggle geometry");
  assert.match(compactToggle[1], /\bwidth:\s*36px/);
  assert.match(compactToggle[1], /\bmin-height:\s*36px/);
  assert.match(compactToggle[1], /\bmargin-inline-start:\s*2px/);
  assert.match(compactToggle[1], /\bpadding:\s*0/);
  assert.match(compactToggle[1], /\bjustify-content:\s*center/);
  assert.match(app, /icon\("panel-left-close",\s*18\)[\s\S]*icon\("panel-left-open",\s*18\)/);
  assert.doesNotMatch(css, /\.is-compact\s+\.sidebar-toggle\s+\.icon\s*\{[^}]*transform:/);
});
