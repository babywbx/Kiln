import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const channels = await readFile("modules/httpserver/admin/assets/views/channels.js", "utf8");
const detail = await readFile("modules/httpserver/admin/assets/views/channel-detail.js", "utf8");
const css = await readFile("modules/httpserver/admin/assets/app.css", "utf8");

test("the channel row toggle warms up an idle channel and stops a live session", () => {
  assert.match(channels, /const active = Boolean\(sessionFor\(channel\.id\)\)/);
  assert.match(channels, /await endpoints\.stopSession\(channel\.id\)/);
  assert.match(channels, /await endpoints\.warmupChannel\(channel\.id\)/);
  assert.match(channels, /iconButton\(active \? "square" : "play"/);
  assert.match(channels, /iconName: active \? "square" : "play"/);
  assert.match(channels, /vt\(active \? "channels\.stopNamed" : "channels\.startNamed"/);
});

test("both layouts share the toggle and repaint it when the session state changes", () => {
  assert.match(channels, /class: "row-actions" \},\s*entry\.toggle,/);
  assert.match(channels, /class: "record-actions" \},[\s\S]*?entry\.cardToggle,/);
  assert.match(channels, /entry\.toggle = swap\(entry\.toggle, rowToggle\(entry\.channel\)\)/);
  assert.match(channels, /entry\.cardToggle = swap\(entry\.cardToggle, cardToggle\(entry\.channel\)\)/);
  assert.match(channels, /document\.activeElement === control \|\| control\.dataset\.restoreFocus === "true"/);
  assert.match(channels, /if \(restoreFocus\) next\.focus\(\)/);
});

test("the channel name opens the preview instead of routing to the configuration page", () => {
  assert.match(channels, /class: "cell-link", type: "button"[^)]*onClick: \(\) => previewChannel\(channel\)/);
  assert.doesNotMatch(channels, /class: "cell-link", href/);
  assert.match(channels, /h\("td", \{\}, previewCell\(channel\)\)/);
  assert.match(channels, /class: "record-head" \}, previewCell\(channel\)/);
  assert.match(channels, /linkButton\(vt\("channels\.configure"\), route/);
  assert.match(channels, /linkButton\(vt\("channels\.configureChannel"\), route/);
});

test("the configuration page keeps its own preview entry", () => {
  assert.match(detail, /vt\("channel\.openPreview"\)[^;]*onClick: \(\) => previewChannel\(channel\)/);
});

test("the redirect upgrade setting is editable and affects source checks", () => {
  assert.match(detail, /const upgradeRedirectsInput = h\("input", \{ name: "upgrade_insecure_redirects", type: "checkbox"/);
  assert.match(detail, /upgrade_insecure_redirects: upgradeRedirectsInput\.checked/);
  assert.match(detail, /headersInput, upgradeRedirectsInput, packagerSelect/);
  assert.match(detail, /vt\("channel\.upgradeRedirects"\)/);
});

test("the channel cell stays styled once it becomes a button", () => {
  assert.match(css, /button\.cell-link\s*\{[^}]*background:\s*none[^}]*cursor:\s*pointer/);
  assert.match(css, /button\.cell-link:disabled\s*\{[^}]*cursor:\s*default/);
  assert.match(css, /\.cell-link:hover:not\(:disabled\) strong/);
});
