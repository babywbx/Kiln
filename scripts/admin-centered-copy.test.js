import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const assetsRoot = path.resolve("modules/httpserver/admin/assets");
const i18n = await readFile(path.join(assetsRoot, "core/i18n.js"), "utf8");
const viewI18n = await readFile(path.join(assetsRoot, "core/view-i18n.js"), "utf8");
const css = await readFile(path.join(assetsRoot, "app.css"), "utf8");

const centeredCopy = [
  [i18n, [
    "error.tryAgain",
    "egress.proxy.emptyDescription",
    "egress.rule.emptyDescription",
    "epg.emptySourcesDescription",
    "epg.match.emptyDescription",
    "overview.session.emptyDescription",
  ]],
  [viewI18n, [
    "access.emptyLogsHint",
    "access.emptyTokensHint",
    "apiToken.auditEmptyHint",
    "apiToken.emptyHint",
    "channel.dormantHint",
    "channel.noCandidatesHint",
    "channel.noLogosHint",
    "channels.emptyHint",
    "channels.noMatchHint",
  ]],
];

test("copy rendered inside centered empty states carries no sentence-final period", () => {
  const offenders = [];
  for (const [source, keys] of centeredCopy) {
    for (const key of keys) {
      const pattern = new RegExp(`"${key.replaceAll(".", "\\.")}":\\s*"([^"]*)"`, "g");
      for (const match of source.matchAll(pattern)) {
        if (/[。．.]$/.test(match[1])) offenders.push(`${key} → ${match[1]}`);
      }
    }
  }
  assert.deepEqual(offenders, [], "a trailing full-width period leaves empty space that pulls centered text off axis");
});

test("every centered key still exists in all three locales", () => {
  for (const [source, keys] of centeredCopy) {
    for (const key of keys) {
      const pattern = new RegExp(`"${key.replaceAll(".", "\\.")}":`, "g");
      assert.equal([...source.matchAll(pattern)].length, 3, `${key} must be translated in every locale`);
    }
  }
});

test("centered and metric copy balances its line breaks", () => {
  for (const selector of [".empty-state p", ".metric-label", ".metric-meta"]) {
    const rule = css.split("\n").find((line) => line.startsWith(`${selector} `) || line.startsWith(`${selector}{`));
    assert.ok(rule, `${selector} rule is missing from app.css`);
    assert.match(rule, /text-wrap:\s*balance/, `${selector} needs balanced wrapping so short copy does not leave an orphan word`);
  }
});
