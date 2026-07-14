import assert from "node:assert/strict";
import test from "node:test";

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

class FakeBroadcastChannel {
  static channels = new Set();
  static delayFor = () => 0;

  constructor() {
    this.listeners = new Set();
    FakeBroadcastChannel.channels.add(this);
  }

  addEventListener(type, listener) {
    if (type === "message") this.listeners.add(listener);
  }

  postMessage(data) {
    for (const channel of FakeBroadcastChannel.channels) {
      if (channel === this) continue;
      const deliver = () => {
        for (const listener of channel.listeners) listener({ data });
      };
      const delay = FakeBroadcastChannel.delayFor(data);
      if (delay > 0) setTimeout(deliver, delay);
      else queueMicrotask(deliver);
    }
  }
}

function jwt(iat, label) {
  const payload = Buffer.from(JSON.stringify({ iat, label })).toString("base64url");
  return `header.${payload}.signature`;
}

test("admin tabs validate shared sessions and never revive a signed-out session", async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => { globalThis.fetch = originalFetch; });
  globalThis.BroadcastChannel = FakeBroadcastChannel;
  globalThis.window = {
    BroadcastChannel: FakeBroadcastChannel,
    localStorage: new MemoryStorage(),
    sessionStorage: new MemoryStorage(),
  };
  const olderToken = jwt(100, "older-valid");
  const newerToken = jwt(300, "newer-valid");
  const newerInvalidToken = jwt(200, "newer-invalid");
  const requests = [];
  globalThis.fetch = async (_url, options = {}) => {
    const authorization = options.headers instanceof Headers
      ? options.headers.get("Authorization") || ""
      : options.headers?.Authorization || "";
    requests.push(authorization);
    const valid = authorization === `Bearer ${olderToken}` || authorization === `Bearer ${newerToken}`;
    return new Response(JSON.stringify({ role: valid ? "admin" : "user" }), {
      status: valid ? 200 : 401,
      headers: { "Content-Type": "application/json" },
    });
  };

  const first = await import(`../modules/httpserver/admin/assets/core/api.js?tab=first-${Date.now()}`);
  const second = await import(`../modules/httpserver/admin/assets/core/api.js?tab=second-${Date.now()}`);
  let secondAvailable = 0;
  second.setSessionAvailableHandler(() => { secondAvailable += 1; });

  first.saveSession(olderToken, "2999-01-01T00:00:00Z", false);
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.equal(second.hasSession(), true);
  assert.equal(secondAvailable, 1);

  const third = await import(`../modules/httpserver/admin/assets/core/api.js?tab=third-${Date.now()}`);
  assert.equal(await third.requestSharedSession(10), true);
  assert.equal(third.hasSession(), true);

  second.saveSession(newerInvalidToken, "2999-01-01T00:00:00Z", false);
  await new Promise((resolve) => setTimeout(resolve, 5));
  const fourth = await import(`../modules/httpserver/admin/assets/core/api.js?tab=fourth-${Date.now()}`);
  assert.equal(await fourth.requestSharedSession(10), true);
  assert.equal(fourth.hasSession(), true);

  let externalLogouts = 0;
  third.setExternalLogoutHandler(() => { externalLogouts += 1; });
  first.clearSession();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(third.hasSession(), false);
  assert.equal(externalLogouts, 1);

  first.saveSession(olderToken, "2999-01-01T00:00:00Z", false);
  await new Promise((resolve) => setTimeout(resolve, 5));
  const fifth = await import(`../modules/httpserver/admin/assets/core/api.js?tab=fifth-${Date.now()}`);
  let fifthAvailable = 0;
  fifth.setSessionAvailableHandler(() => { fifthAvailable += 1; });
  FakeBroadcastChannel.delayFor = (message) => message.type === "session" ? 30 : 0;
  assert.equal(await fifth.requestSharedSession(5), false);
  first.clearSession();
  await new Promise((resolve) => setTimeout(resolve, 50));
  assert.equal(fifth.hasSession(), false);
  assert.equal(fifthAvailable, 0);

  FakeBroadcastChannel.delayFor = (message) => message.type === "signed-in" ? 30 : 0;
  const sixth = await import(`../modules/httpserver/admin/assets/core/api.js?tab=sixth-${Date.now()}`);
  first.saveSession(olderToken, "2999-01-01T00:00:00Z", false);
  first.clearSession();
  await new Promise((resolve) => setTimeout(resolve, 50));
  assert.equal(sixth.hasSession(), false);

  first.clearSession();
  await new Promise((resolve) => setTimeout(resolve, 0));
  const seventh = await import(`../modules/httpserver/admin/assets/core/api.js?tab=seventh-${Date.now()}`);
  FakeBroadcastChannel.delayFor = (message) => message.type === "signed-in" && message.token === olderToken ? 30 : 0;
  first.saveSession(olderToken, "2999-01-01T00:00:00Z", false);
  second.saveSession(newerToken, "2999-01-01T00:00:00Z", false);
  await new Promise((resolve) => setTimeout(resolve, 50));
  assert.equal(seventh.hasSession(), true, JSON.stringify(requests));
  requests.length = 0;
  await seventh.api("/probe", { suppressUnauthorized: true });
  assert.deepEqual(requests, [`Bearer ${newerToken}`]);
});
