import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const distUrl = new URL("../dist/", import.meta.url);

async function readPage(path) {
  return readFile(new URL(path, distUrl), "utf8");
}

function tags(html, name) {
  return html.match(new RegExp(`<${name}\\b[^>]*>`, "gi")) ?? [];
}

function attrs(tag) {
  return Object.fromEntries(
    [...tag.matchAll(/([:\w-]+)=(?:"([^"]*)"|'([^']*)')/g)].map((match) => [
      match[1],
      match[2] ?? match[3],
    ]),
  );
}

function metaContent(html, attribute, value) {
  return tags(html, "meta")
    .map(attrs)
    .find((attributes) => attributes[attribute] === value)?.content;
}

function links(html, rel) {
  return tags(html, "link")
    .map(attrs)
    .filter((attributes) => attributes.rel === rel);
}

function jsonLd(html) {
  return [...html.matchAll(/<script\b[^>]*type="application\/ld\+json"[^>]*>([\s\S]*?)<\/script>/gi)]
    .map((match) => JSON.parse(match[1]));
}

test("landing and 404 pages expose the skip-link target", async () => {
  for (const page of ["index.html", "en/index.html", "404.html"]) {
    const html = await readPage(page);
    assert.match(html, /<a\b[^>]*href="#main-content"/);
    assert.match(html, /<main\b[^>]*id="main-content"/);
  }
});

test("English pages emit English social and structured metadata", async () => {
  for (const page of ["en/index.html", "en/guide/variants/index.html"]) {
    const html = await readPage(page);
    assert.equal(metaContent(html, "property", "og:locale"), "en");
    assert.equal(metaContent(html, "property", "og:image:alt"), "Kiln documentation preview");
    assert.equal(metaContent(html, "name", "twitter:image:alt"), "Kiln documentation preview");
    assert.ok(jsonLd(html).length > 0);
    assert.ok(jsonLd(html).every((entry) => entry.inLanguage === "en"));
  }
});

test("Chinese pages retain Chinese social and structured metadata", async () => {
  const html = await readPage("index.html");
  assert.equal(metaContent(html, "property", "og:locale"), "zh-CN");
  assert.equal(metaContent(html, "property", "og:image:alt"), "Kiln 文档预览");
  assert.equal(metaContent(html, "name", "twitter:image:alt"), "Kiln 文档预览");
  assert.ok(jsonLd(html).length > 0);
  assert.ok(jsonLd(html).every((entry) => entry.inLanguage === "zh-CN"));
});

test("search combobox exposes a name, focus indicator, and live status", async () => {
  const html = await readPage("index.html");
  const combobox = tags(html, "input")
    .map(attrs)
    .find((attributes) => attributes.role === "combobox");
  assert.equal(combobox?.["aria-label"], "搜索文档");
  assert.match(combobox?.class ?? "", /(?:^|\s)focus-visible:outline-2(?:\s|$)/);

  const statusTag = tags(html, "p").find((tag) => /\bdata-search-empty(?:\s|=|>)/.test(tag));
  const status = attrs(statusTag ?? "");
  assert.equal(status?.role, "status");
  assert.equal(status?.["aria-live"], "polite");
  assert.equal(status?.["aria-atomic"], "true");
});

test("404 is noindex without canonical or language alternates", async () => {
  const html = await readPage("404.html");
  assert.equal(metaContent(html, "name", "robots"), "noindex");
  assert.equal(links(html, "canonical").length, 0);
  assert.equal(links(html, "alternate").filter((link) => "hreflang" in link).length, 0);
});
