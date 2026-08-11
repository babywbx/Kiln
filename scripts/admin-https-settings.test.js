import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const assetsRoot = path.resolve("modules/httpserver/admin/assets");
const i18n = await readFile(path.join(assetsRoot, "core/i18n.js"), "utf8");
const settings = await readFile(path.join(assetsRoot, "views/settings.js"), "utf8");
const css = await readFile(path.join(assetsRoot, "app.css"), "utf8");
const dockerfile = await readFile(path.resolve("deploy/docker/Dockerfile"), "utf8");

const httpsKeys = [
  "settings.httpsTitle",
  "settings.httpsDescription",
  "settings.httpsToggle",
  "settings.httpsToggleAria",
  "settings.httpsToggleHint",
  "settings.httpsRestartHint",
  "settings.httpsCertificate",
  "settings.httpsCertificateFile",
  "settings.httpsCertificateSelfSigned",
  "settings.httpsCertificateError",
  "settings.httpsCertificateErrorHint",
  "settings.httpsCertificateHosts",
  "settings.httpsCertificateExpiry",
];

test("every HTTPS settings string is translated in all three locales", () => {
  for (const key of httpsKeys) {
    const matches = [...i18n.matchAll(new RegExp(`"${key.replaceAll(".", "\\.")}":`, "g"))];
    assert.equal(matches.length, 3, `${key} must exist in zh-Hans, zh-Hant, and en`);
  }
});

test("the HTTPS toggle travels with the settings save instead of applying silently", () => {
  assert.match(settings, /h\(\s*"label",\s*\{ class: "list-item check-row" \}/, "the visible row and checkbox must share one label target");
  assert.match(settings, /tls_enabled: tlsToggle\.checked/, "saving settings must carry the toggle state");
  assert.match(settings, /tlsToggle\.addEventListener\("change", touch\)/, "flipping the toggle must arm the save button");
  assert.match(settings, /settings\.httpsRestartHint/, "the operator must be told a restart is required");
});

test("the certificate host list stacks instead of colliding with its label", () => {
  assert.match(settings, /stackedRow\(i18n\.t\("settings\.httpsCertificateHosts"\)/, "a long host list needs its own line");
});

test("HTTPS cannot be enabled while its certificate is unavailable", () => {
  assert.match(settings, /disabled: Boolean\(tlsError && !data\.tls_enabled\)/);
  assert.match(settings, /if \(tlsError && tlsToggle\.checked\)/);
  assert.match(settings, /notice\(i18n\.t\("settings\.httpsCertificateErrorHint"\), "danger"/);
});

test("core and full container healthchecks accept HTTP and HTTPS", () => {
  assert.match(
    dockerfile,
    /--no-check-certificate --spider https:\/\/127\.0\.0\.1:8080\/healthz \|\| wget[^\n]*http:\/\/127\.0\.0\.1:8080\/healthz/,
  );
});

test("the sidebar status dot keeps its place while only the copy centres", () => {
  assert.match(css, /\.instance \.dot \{ position: absolute; left: 11px; \}/, "the dot stays pinned to the left edge");
  assert.match(css, /\.instance > span:not\(\.dot\)/, "centring must not also pad the dot into an ellipse");
});

test("a flush card body is flush at the top too", () => {
  assert.match(
    css,
    /\.card > \.card-body\.is-flush:first-child \{ padding: 0; \}/,
    "the first-child rule outranks .is-flush, so it needs an explicit override or flush cards keep 24px of dead space",
  );
});
