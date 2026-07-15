import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const source = await readFile("modules/httpserver/admin/assets/views/egress.js", "utf8");

test("egress tables stack vertically instead of sharing a narrow row", () => {
  assert.doesNotMatch(source, /class:\s*"split-even"/);
});
