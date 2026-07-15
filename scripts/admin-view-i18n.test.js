import assert from "node:assert/strict";
import test from "node:test";

import { i18n } from "../modules/httpserver/admin/assets/core/i18n.js";
import { viewMessageKeys, vt } from "../modules/httpserver/admin/assets/core/view-i18n.js";

const LOCALES = ["zh-Hans", "zh-Hant", "en"];

test("view translations expose the same keys in every supported locale", () => {
  const expected = viewMessageKeys("zh-Hans");
  assert.ok(expected.includes("channels.title"));
  assert.ok(expected.includes("channel.invalidSource"));
  assert.ok(expected.includes("access.revokeWarning"));

  for (const locale of LOCALES) assert.deepEqual(viewMessageKeys(locale), expected, `${locale} view keys differ`);
});

test("every view key resolves without falling back to the key itself", () => {
  const previous = i18n.locale;
  try {
    for (const locale of LOCALES) {
      i18n.setLocale(locale);
      for (const key of viewMessageKeys(locale)) assert.notEqual(vt(key), key, `${locale} is missing ${key}`);
    }
  } finally {
    i18n.setLocale(previous);
  }
});

test("Traditional Chinese keeps critical channel labels fully localized", () => {
  const previous = i18n.locale;
  try {
    i18n.setLocale("zh-Hant");
    assert.equal(vt("channels.header.status"), "狀態");
    assert.match(vt("import.contentPlaceholder"), /頻道名稱/);
    assert.equal(vt("channel.packagerFFmpeg"), "僅 ffmpeg");
    assert.equal(vt("common.listSeparator"), "、");
  } finally {
    i18n.setLocale(previous);
  }
});

test("view translations interpolate values for each locale", () => {
  const previous = i18n.locale;
  try {
    const expected = { "zh-Hans": "3 个频道", "zh-Hant": "3 個頻道", en: "3 channels" };
    for (const locale of LOCALES) {
      i18n.setLocale(locale);
      assert.equal(vt("common.channels", { count: 3 }), expected[locale]);
    }
  } finally {
    i18n.setLocale(previous);
  }
});

test("guide source labels stay compact and channel examples stay neutral", () => {
  const previous = i18n.locale;
  const automatic = { "zh-Hans": "自动", "zh-Hant": "自動", en: "Auto" };
  const placeholders = { "zh-Hans": "频道名称", "zh-Hant": "頻道名稱", en: "Channel name" };
  const guideSources = { "zh-Hans": "节目单源", "zh-Hant": "節目表來源", en: "Guide source" };
  const broadcasterName = /CCTV|TVBS?|BBC/i;
  try {
    for (const locale of LOCALES) {
      i18n.setLocale(locale);
      assert.equal(vt("channel.epgAny"), automatic[locale]);
      assert.equal(vt("channel.epgNamePlaceholder"), placeholders[locale]);
      assert.equal(vt("channel.epgSource"), guideSources[locale]);
      assert.doesNotMatch(vt("channel.epgNamePlaceholder"), broadcasterName);
      assert.doesNotMatch(i18n.t("epg.preset.hongKongDescription"), broadcasterName);
      assert.doesNotMatch(i18n.t("epg.preset.taiwanDescription"), broadcasterName);
    }
  } finally {
    i18n.setLocale(previous);
  }
});
