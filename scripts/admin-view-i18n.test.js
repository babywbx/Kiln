import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import { i18n, messageKeys } from "../modules/httpserver/admin/assets/core/i18n.js";
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

test("admin terminology stays precise and consistent across locales", () => {
  const previous = i18n.locale;
  const expected = {
    "zh-Hans": {
      streamSource: "节目源", guideSource: "节目单源", revoke: "吊销", clientAddress: "客户端地址",
      connectionTest: "连接测试", guideSourceID: "节目单源 ID", channelID: "频道 ID",
    },
    "zh-Hant": {
      streamSource: "節目來源", guideSource: "節目表來源", revoke: "撤銷", clientAddress: "用戶端位址",
      connectionTest: "連線測試", guideSourceID: "節目表來源 ID", channelID: "頻道 ID",
    },
    en: {
      streamSource: "Stream source", guideSource: "Guide source", revoke: "Revoke", clientAddress: "Client Address",
      connectionTest: "Connection Test", guideSourceID: "Guide Source ID", channelID: "Channel ID",
    },
  };
  try {
    for (const locale of LOCALES) {
      i18n.setLocale(locale);
      const terms = expected[locale];
      assert.equal(vt("channel.source"), terms.streamSource);
      assert.equal(vt("channel.epgSource"), terms.guideSource);
      assert.equal(vt("access.revokeAction"), terms.revoke);
      assert.equal(vt("access.remote"), terms.clientAddress);
      assert.equal(i18n.t("egress.test.title"), terms.connectionTest);
      assert.equal(i18n.t("epg.form.id"), terms.guideSourceID);
      assert.equal(i18n.t("egress.ruleKind.channelId"), terms.channelID);
    }
  } finally {
    i18n.setLocale(previous);
  }
});

test("admin copy does not reintroduce retired or misleading terms", () => {
  const previous = i18n.locale;
  const forbidden = /限定节目单源|來源伺服器|来源服务器|Programme Source|Test & Apply|测试并应用|測試並套用|停用访问密钥|停用存取金鑰|Source Address|CCTV|TVBS?|BBC/i;
  try {
    for (const locale of LOCALES) {
      i18n.setLocale(locale);
      for (const key of messageKeys(locale)) assert.doesNotMatch(i18n.t(key), forbidden, `${locale} core copy: ${key}`);
      for (const key of viewMessageKeys(locale)) assert.doesNotMatch(vt(key), forbidden, `${locale} view copy: ${key}`);
    }
  } finally {
    i18n.setLocale(previous);
  }

  for (const relativePath of [
    "../modules/httpserver/admin/assets/core/api.js",
    "../modules/httpserver/admin/assets/core/router.js",
  ]) {
    const source = readFileSync(new URL(relativePath, import.meta.url), "utf8");
    assert.doesNotMatch(source, /[\u3400-\u9fff]/, `${relativePath} contains hard-coded Chinese UI copy`);
  }
});
