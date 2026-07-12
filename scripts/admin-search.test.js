import assert from "node:assert/strict";
import test from "node:test";

import {
  buildSearchIndex,
  searchIndex,
  normalizeSearch,
  distanceWithinTwo,
} from "../modules/httpserver/admin/assets/core/search.js";

function referenceDistance(a, b) {
  const d = Array.from({ length: a.length + 1 }, (_, i) =>
    Array.from({ length: b.length + 1 }, (_, j) => (i === 0 ? j : j === 0 ? i : 0)),
  );
  for (let i = 1; i <= a.length; i += 1) {
    for (let j = 1; j <= b.length; j += 1) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1;
      let value = Math.min(d[i - 1][j] + 1, d[i][j - 1] + 1, d[i - 1][j - 1] + cost);
      if (i > 1 && j > 1 && a[i - 1] === b[j - 2] && a[i - 2] === b[j - 1]) {
        value = Math.min(value, d[i - 2][j - 2] + 1);
      }
      d[i][j] = value;
    }
  }
  return d[a.length][b.length];
}

const channels = [
  { id: "channel-uhd", title: "翡翠臺 4K", group: "Entertainment" },
  { id: "channel-news", title: "新聞台", group: "News" },
  { id: "chongqing", title: "重庆卫视", group: "Regional" },
  { id: "xiamen", title: "厦门综合", group: "Regional" },
  { id: "plain", title: "Sports HD", group: "Sports" },
];

const index = await buildSearchIndex(channels);

async function ids(query) {
  return (await searchIndex(index, query)).map((item) => item.id);
}

test("matches Mandarin pinyin regardless of separators", async () => {
  for (const query of ["feicuitai", "fei cui tai", "fei'cui'tai", "FEI-CUI-TAI", "fei.cui.tai"]) {
    assert.equal((await ids(query))[0], "channel-uhd", `query ${query} should reach the channel`);
  }
});

test("matches pinyin initials", async () => {
  assert.equal((await ids("fct"))[0], "channel-uhd");
  assert.equal((await ids("cqws"))[0], "chongqing");
});

test("matches Cantonese jyutping", async () => {
  assert.equal((await ids("feiceoitoi"))[0], "channel-uhd");
  assert.equal((await ids("fei ceoi toi"))[0], "channel-uhd");
});

test("bridges simplified and traditional forms", async () => {
  assert.equal((await ids("翡翠台"))[0], "channel-uhd");
  assert.equal((await ids("翡翠臺"))[0], "channel-uhd");
  assert.equal((await ids("新闻台"))[0], "channel-news");
});

test("resolves heteronyms to every common reading", async () => {
  assert.equal((await ids("chongqing"))[0], "chongqing");
  assert.equal((await ids("zhongqing"))[0], "chongqing");
  assert.equal((await ids("xiamen"))[0], "xiamen");
  assert.equal((await ids("shamen"))[0], "xiamen");
});

test("tolerates a transposed or dropped letter", async () => {
  assert.equal((await ids("feicuitia"))[0], "channel-uhd");
  assert.equal((await ids("feicutai"))[0], "channel-uhd");
});

test("still matches plain latin titles and ids", async () => {
  assert.equal((await ids("sports"))[0], "plain");
  assert.equal((await ids("channel-news"))[0], "channel-news");
});

test("returns every channel for an empty query", async () => {
  assert.equal((await ids("")).length, channels.length);
  assert.equal((await ids("   ")).length, channels.length);
});

test("drops results that match nothing", async () => {
  assert.deepEqual(await ids("zzzzzqqqqq"), []);
});

test("counts an adjacent transposition as one edit", () => {
  assert.equal(distanceWithinTwo("feicuitia", "feicuitai"), 1);
  assert.equal(distanceWithinTwo("zhongqing", "zhonqging"), 1);
  assert.equal(distanceWithinTwo("abcdef", "abdcef"), 1);
});

test("agrees with a full-matrix Damerau-Levenshtein and never exceeds the sentinel", () => {
  const alphabet = "abcde";
  const pick = (length) =>
    Array.from({ length }, () => alphabet[Math.floor(Math.random() * alphabet.length)]).join("");
  for (let round = 0; round < 20000; round += 1) {
    const a = pick(1 + Math.floor(Math.random() * 8));
    const b = pick(1 + Math.floor(Math.random() * 8));
    const got = distanceWithinTwo(a, b);
    const want = referenceDistance(a, b);
    assert.ok(got <= 3, `${a} vs ${b} returned ${got}, breaking the sentinel contract`);
    assert.equal(got, want <= 2 ? want : 3, `${a} vs ${b}`);
  }
});

test("normalizes punctuation and width to a single form", () => {
  assert.equal(normalizeSearch("  Fei'Cui  Tai! "), "fei cui tai");
  assert.equal(normalizeSearch("ＴＶ－４Ｋ"), "tv 4k");
});
