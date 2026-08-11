import assert from "node:assert/strict";
import test from "node:test";

import { api } from "../modules/httpserver/admin/assets/core/api.js";

test("admin reads retry transient failures without retrying writes", async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => { globalThis.fetch = originalFetch; });

  const responses = [];
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    const response = new Response(JSON.stringify(calls < 3 ? { error: { message: "retry" } } : { ok: true }), {
      status: calls < 3 ? 503 : 200,
      headers: { "Content-Type": "application/json" },
    });
    responses.push(response);
    return response;
  };

  assert.deepEqual(await api("/read"), { ok: true });
  assert.equal(calls, 3);
  assert.equal(responses[0].bodyUsed, true);

  calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    return new Response(JSON.stringify({ error: { message: "unavailable" } }), {
      status: 503,
      headers: { "Content-Type": "application/json" },
    });
  };
  await assert.rejects(api("/write", { method: "POST", body: "{}" }), (error) => error.status === 503);
  assert.equal(calls, 1);
});

test("admin read retries preserve navigation aborts", async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => { globalThis.fetch = originalFetch; });
  globalThis.fetch = async () => { throw new TypeError("offline"); };

  const controller = new AbortController();
  const pending = api("/read", { signal: controller.signal });
  setTimeout(() => controller.abort(), 10);
  await assert.rejects(pending, (error) => error.name === "AbortError");
});
