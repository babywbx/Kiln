import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

import { lucideIcons } from "../modules/httpserver/admin/assets/third_party/lucide-icons.mjs";

const assetsRoot = path.resolve("modules/httpserver/admin/assets");
const iconPatterns = [
  /\bicon\(\s*["']([^"']+)["']/g,
  /\biconName:\s*["']([^"']+)["']/g,
  /\btrailingIcon:\s*["']([^"']+)["']/g,
  /\bicon:\s*["']([^"']+)["']/g,
];

async function javascriptFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = await Promise.all(entries.map(async (entry) => {
    const location = path.join(directory, entry.name);
    if (entry.isDirectory()) return entry.name === "third_party" ? [] : javascriptFiles(location);
    return entry.isFile() && entry.name.endsWith(".js") ? [location] : [];
  }));
  return files.flat();
}

test("every statically referenced admin icon exists in the bundled subset", async () => {
  const missing = [];
  for (const file of await javascriptFiles(assetsRoot)) {
    const source = await readFile(file, "utf8");
    for (const pattern of iconPatterns) {
      for (const match of source.matchAll(pattern)) {
        if (!lucideIcons[match[1]]) missing.push(`${path.relative(assetsRoot, file)}: ${match[1]}`);
      }
    }
  }
  assert.deepEqual(missing, []);
});
