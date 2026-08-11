import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const kit = await readFile(path.resolve("modules/httpserver/admin/assets/ui/kit.js"), "utf8");

function bodyOf(signature) {
  const start = kit.indexOf(signature);
  assert.notEqual(start, -1, `${signature} is missing from kit.js`);
  return kit.slice(start, kit.indexOf("\n}", start));
}

test("every button factory that renders an icon records it for setBusy", () => {
  for (const factory of ["export function button(", "export function iconButton("]) {
    assert.match(bodyOf(factory), /dataset:\s*(\{[^}]*icon:|iconName \?)/, `${factory} must set dataset.icon so setBusy swaps the icon instead of prepending a second one`);
  }
});

test("iconButton records its icon size and renders at that size", () => {
  const body = bodyOf("export function iconButton(");
  assert.match(body, /const size = options\.size \|\| 18;/);
  assert.match(body, /iconSize:\s*String\(size\)/);
  assert.match(body, /icon\(iconName,\s*size\)/);
});

test("setBusy swaps and restores the icon at its recorded size", () => {
  const body = bodyOf("export function setBusy(");
  assert.match(body, /const size = Number\(control\.dataset\.iconSize\) \|\| 16;/);
  assert.match(body, /icon\("loader-circle",\s*size\)/);
  assert.match(body, /spinning\.replaceWith\(icon\(name,\s*size\)\)/);
});
