import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const assetsRoot = path.resolve("modules/httpserver/admin/assets");

async function assetFiles(dir) {
  const found = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    if (entry.name === "third_party") continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) found.push(...(await assetFiles(full)));
    else if (entry.name.endsWith(".js")) found.push(full);
  }
  return found;
}

const files = await assetFiles(assetsRoot);

const secureOnly = [
  { name: "crypto.randomUUID", pattern: /(?<!\?\.)\bcrypto\.randomUUID\s*\(/ },
  { name: "crypto.subtle", pattern: /(?<!\?\.)\bcrypto\.subtle\b/ },
  { name: "navigator.clipboard", pattern: /\bnavigator\.clipboard\s*\.\s*\w/ },
];

test("secure-context-only APIs are never called unguarded", async () => {
  const offenders = [];
  for (const file of files) {
    const source = await readFile(file, "utf8");
    for (const { name, pattern } of secureOnly) {
      for (const [index, line] of source.split("\n").entries()) {
        if (!pattern.test(line)) continue;
        if (/\bif\s*\(\s*navigator\.clipboard\s*\)/.test(line)) continue;
        offenders.push(`${path.relative(assetsRoot, file)}:${index + 1} → ${name}`);
      }
    }
  }
  assert.deepEqual(
    offenders,
    [],
    "these APIs are undefined over plain http on a LAN address, so the admin UI throws on any deployment that is not https or localhost",
  );
});

test("copyText falls back when the async clipboard is unavailable", async () => {
  const overlay = await readFile(path.join(assetsRoot, "ui/overlay.js"), "utf8");
  assert.match(overlay, /if\s*\(navigator\.clipboard\)/, "copyText must probe for the clipboard before using it");
  assert.match(overlay, /document\.execCommand\("copy"\)/, "copyText needs a non-secure-context fallback");
  assert.match(overlay, /finally\s*\{[\s\S]*?area\.remove\(\);[\s\S]*?active\?\.focus\?\.\(\)/, "the fallback must clean up and restore keyboard focus even when copying throws");
});

test("field ids come from a document-local counter, not crypto", async () => {
  const kit = await readFile(path.join(assetsRoot, "ui/kit.js"), "utf8");
  assert.match(kit, /`f-\$\{\+\+fieldSeq\}`/, "field ids only need document uniqueness");
});
