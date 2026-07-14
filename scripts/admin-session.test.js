import assert from "node:assert/strict";
import test from "node:test";

import { clearSession, hasSession, loadSession, remembersSession, saveSession } from "../modules/httpserver/admin/assets/core/api.js";

class MemoryStorage {
  constructor() {
    this.values = new Map();
  }

  getItem(key) {
    return this.values.get(key) ?? null;
  }

  setItem(key, value) {
    this.values.set(key, String(value));
  }

  removeItem(key) {
    this.values.delete(key);
  }
}

test("admin session supports remembered and tab-scoped bearer fallbacks", () => {
  globalThis.window = { localStorage: new MemoryStorage(), sessionStorage: new MemoryStorage() };

  saveSession("remembered", "2999-01-01T00:00:00Z", true);
  assert.equal(remembersSession(), true);
  assert.equal(hasSession(), true);
  assert.equal(loadSession(), "remembered");

  saveSession("tab-only", "2999-01-01T00:00:00Z", false);
  assert.equal(remembersSession(), false);
  assert.equal(loadSession(), "tab-only");
  clearSession();
  assert.equal(hasSession(), false);
});

test("admin session tolerates blocked web storage", () => {
  const blocked = {};
  Object.defineProperties(blocked, {
    localStorage: { get: () => { throw new Error("blocked"); } },
    sessionStorage: { get: () => { throw new Error("blocked"); } },
  });
  globalThis.window = blocked;

  assert.doesNotThrow(() => saveSession("cookie-backed", "2999-01-01T00:00:00Z", false));
  assert.equal(hasSession(), true);
  assert.equal(remembersSession(), false);
  assert.doesNotThrow(clearSession);
});
