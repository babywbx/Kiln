import assert from "node:assert/strict";
import test from "node:test";

import { LOCALE_OPTIONS, createI18n, resolveLocale } from "../modules/httpserver/admin/assets/core/i18n.js";
import { classifyLoginError, validateCredentials } from "../modules/httpserver/admin/assets/views/login-model.js";

const TRANSLATION_KEYS = [
  "meta.loginTitle",
  "shared.skipToContent",
  "login.language",
  "login.eyebrow",
  "login.title",
  "login.description",
  "login.username",
  "login.password",
  "login.remember",
  "login.submit",
  "login.submitting",
  "login.error.usernameRequired",
  "login.error.passwordRequired",
  "login.error.invalidCredentials",
  "login.error.adminRequired",
  "login.error.rateLimited",
  "login.error.invalidRequest",
  "login.error.accessDenied",
  "login.error.serviceUnavailable",
  "login.error.serverError",
  "login.error.networkError",
  "login.error.unknown",
];

function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, String(value)),
  };
}

test("resolveLocale prefers saved locale and normalizes browser language tags", () => {
  assert.equal(resolveLocale("en", ["zh-TW"]), "en");
  assert.equal(resolveLocale("", ["zh-CN"]), "zh-Hans");
  assert.equal(resolveLocale("", ["zh-SG"]), "zh-Hans");
  assert.equal(resolveLocale("", ["zh-TW"]), "zh-Hant");
  assert.equal(resolveLocale("", ["zh-HK"]), "zh-Hant");
  assert.equal(resolveLocale("", ["zh-MO"]), "zh-Hant");
  assert.equal(resolveLocale("", ["en-US"]), "en");
  assert.equal(resolveLocale("fr", ["fr-FR", "zh-Hant"]), "zh-Hant");
  assert.equal(resolveLocale("fr", ["de-DE"]), "zh-Hans");
});

test("all supported locales provide every login translation", () => {
  const storage = memoryStorage();
  const i18n = createI18n({ storage, languages: [] });
  assert.deepEqual(LOCALE_OPTIONS.map(({ value }) => value), ["zh-Hans", "zh-Hant", "en"]);

  for (const { value } of LOCALE_OPTIONS) {
    i18n.setLocale(value);
    for (const key of TRANSLATION_KEYS) assert.notEqual(i18n.t(key), key, `${value} is missing ${key}`);
  }
});

test("createI18n remembers an explicit locale selection", () => {
  const storage = memoryStorage();
  const i18n = createI18n({ storage, languages: ["en-US"] });
  assert.equal(i18n.locale, "en");
  i18n.setLocale("zh-Hant");
  assert.equal(createI18n({ storage, languages: ["en-US"] }).locale, "zh-Hant");
});

test("createI18n tolerates storage access failures", () => {
  const storage = {
    getItem() {
      throw new DOMException("Blocked", "SecurityError");
    },
    setItem() {
      throw new DOMException("Blocked", "SecurityError");
    },
  };
  const i18n = createI18n({ storage, languages: ["en-US"] });
  assert.equal(i18n.locale, "en");
  assert.equal(i18n.setLocale("zh-Hant"), "zh-Hant");
});

test("validateCredentials reports each missing field without accepting whitespace usernames", () => {
  assert.deepEqual(validateCredentials("", ""), {
    usernameError: "login.error.usernameRequired",
    passwordError: "login.error.passwordRequired",
    focus: "username",
  });
  assert.deepEqual(validateCredentials("admin", ""), {
    usernameError: "",
    passwordError: "login.error.passwordRequired",
    focus: "password",
  });
  assert.equal(validateCredentials("   ", "secret")?.focus, "username");
  assert.equal(validateCredentials("admin", "secret"), null);
});

test("classifyLoginError maps stable status and codes without using backend messages", () => {
  assert.deepEqual(classifyLoginError({ status: 401, code: "unauthorized", message: "sentinel" }), {
    key: "login.error.invalidCredentials",
    clearPassword: true,
    invalidFields: ["username", "password"],
    focus: "password",
  });
  assert.equal(classifyLoginError({ status: 429, code: "too_many_requests" }).key, "login.error.rateLimited");
  assert.equal(classifyLoginError({ status: 400, code: "invalid_request" }).key, "login.error.invalidRequest");
  assert.equal(classifyLoginError({ status: 403, code: "forbidden" }).key, "login.error.accessDenied");
  assert.equal(classifyLoginError({ status: 503, code: "unavailable" }).key, "login.error.serviceUnavailable");
  assert.equal(classifyLoginError({ status: 500, code: "internal" }).key, "login.error.serverError");
  assert.equal(classifyLoginError(new TypeError("Failed to fetch")).key, "login.error.networkError");
  assert.equal(classifyLoginError(new Error("sentinel")).key, "login.error.unknown");
});
