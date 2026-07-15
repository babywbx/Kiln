import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const source = await readFile("modules/httpserver/admin/assets/views/channels.js", "utf8");
const translations = await readFile("modules/httpserver/admin/assets/core/view-i18n.js", "utf8");

test("M3U import uses direct source URLs without a legacy upstream selector", () => {
  assert.doesNotMatch(source, /default_upstream|import\.defaultUpstream|import\.chooseUpstream/);
  assert.doesNotMatch(source, /entries:\s*parsed/);
  assert.match(source, /content:\s*previewedContent/);
  assert.match(source, /entry\.action/);
  assert.doesNotMatch(translations, /import\.defaultUpstream|import\.chooseUpstream/);
});
