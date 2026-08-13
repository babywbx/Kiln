import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

import { errorSummary } from "../modules/httpserver/admin/assets/core/session-error.js";

const assetsRoot = path.resolve("modules/httpserver/admin/assets");
const i18n = await readFile(path.join(assetsRoot, "core/i18n.js"), "utf8");
const overview = await readFile(path.join(assetsRoot, "views/overview.js"), "utf8");
const overlay = await readFile(path.join(assetsRoot, "ui/overlay.js"), "utf8");
const css = await readFile(path.join(assetsRoot, "app.css"), "utf8");
const accessView = await readFile(path.join(assetsRoot, "views/access.js"), "utf8");
const apiTokensView = await readFile(path.join(assetsRoot, "views/api-tokens.js"), "utf8");
const demo = await readFile("scripts/demo-admin-ui.mjs", "utf8");

test("the summary keeps the head of a wrapped go error", () => {
  assert.equal(
    errorSummary("engine=native but the source cannot be served natively: fetch manifest: remote error: tls: handshake failure"),
    "engine=native but t…",
  );
  assert.equal(errorSummary("upstream timeout"), "upstream timeout");
  assert.equal(errorSummary(""), "");
});

test("a url inside the error never becomes the summary", () => {
  assert.equal(errorSummary(`Get "https://cdn.example.com/index.mpd": timeout`), `Get "https://cdn.ex…`);
});

test("the session table links to the error instead of stretching the row", () => {
  assert.doesNotMatch(overview, /text: session\.last_error/, "a raw error in the cell is what forces the table to scroll");
  assert.match(overview, /button\(errorSummary\(message\)/, "the cell must be an action, not prose");
  assert.match(overview, /onClick: \(\) => showSessionError\(session\.channel_id, message, controlID\)/);
  assert.match(overview, /body: h\("code", \{ class: "code-block mono", text: message \}\)/, "the full message reads as a code block");
  assert.match(overview, /copyButton\(message/, "the full message must be copyable");
});

test("closing the error dialog restores focus after the session table refreshes", () => {
  assert.match(overview, /control\.id = controlID/);
  assert.match(overview, /onClose: \(\) => document\.getElementById\(returnFocusID\)\?\.focus\(\)/);
});

test("no session cell wraps a single label across two lines", () => {
  for (const cell of [/class: "cell-link mono truncate"/, /class: "muted truncate", text: engineLabel/, /class: "mono muted truncate", text: session\.pack_mode/]) {
    assert.match(overview, cell);
  }
});

test("copying confirms on the button instead of behind the dialog", () => {
  assert.match(overlay, /export function copyButton/);
  assert.match(overlay, /copyText\(value, "", \{ announce: false \}\)/, "a toast behind an open modal is invisible");
  assert.match(overlay, /control\.classList\.toggle\("is-copied", copied\)/);
  assert.match(overlay, /icon\(copied \? "check" : "circle-alert", 16\)/);
  assert.match(css, /\.button\.is-copied \{ color: var\(--success\)/, "the confirmation must read as success");
  assert.match(overlay, /vt\(copied \? "common\.copied" : "common\.copyFailed"\)/, "a rejected copy must also report on the button");
  assert.match(css, /\.button\.is-copy-failed \{ color: var\(--danger\)/);
  assert.doesNotMatch(accessView, /button\(vt\("common\.copy"\)/, "every plain copy button shares the same confirmation");
  assert.doesNotMatch(apiTokensView, /button\(vt\("common\.copy"\)/);
  assert.match(accessView, /if \(!\(await copyText\(data\.playlist_url, vt\("access\.playlistCopied"\)\)\)\) return;/, "a failed copy must keep the one-time secret open");
  assert.match(apiTokensView, /if \(await copyText\(token, vt\("apiToken\.copied"\)\)\) closeModal\(\);/);
});

test("the local demo server only listens on loopback", () => {
  assert.match(demo, /\.listen\(port, "127\.0\.0\.1",/);
});

test("auto refresh keeps the table where the operator scrolled it", () => {
  assert.match(overview, /const offset = sessionBody\.querySelector\("\.table-wrap"\)\?\.scrollLeft \|\| 0;/);
  assert.match(overview, /if \(offset\) sessionBody\.querySelector\("\.table-wrap"\)\.scrollLeft = offset;/);
});

test("every session error string is translated in all three locales", () => {
  for (const key of ["overview.error.title", "overview.error.description", "overview.error.view"]) {
    const matches = [...i18n.matchAll(new RegExp(`"${key.replaceAll(".", "\\.")}":`, "g"))];
    assert.equal(matches.length, 3, `${key} must exist in zh-Hans, zh-Hant, and en`);
  }
});
