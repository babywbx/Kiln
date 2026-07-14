import assert from "node:assert/strict";
import test from "node:test";

import { isValidSourceURL, resolveSourceFields } from "../modules/httpserver/admin/assets/core/source-url.js";

test("accepts complete HTTP media URLs", () => {
  assert.equal(isValidSourceURL(" https://media.example/live/index.m3u8?token=abc "), true);
  assert.equal(isValidSourceURL("http://127.0.0.1:8080/live/manifest.mpd"), true);
});

test("rejects unsupported or incomplete source URLs", () => {
  assert.equal(isValidSourceURL("/live/index.m3u8"), false);
  assert.equal(isValidSourceURL("ftp://media.example/live/index.m3u8"), false);
  assert.equal(isValidSourceURL("https://media.example/live/index.m3u8#part"), false);
});

test("keeps matching legacy upstreams so shared headers remain effective", () => {
  assert.deepEqual(
    resolveSourceFields("https://origin.example/api/live/index.m3u8?token=one", [
      { id: "root", base_url: "https://origin.example" },
      { id: "api", base_url: "https://origin.example/api" },
    ]),
    { sourceURL: "", upstream: "api", path: "/live/index.m3u8?token=one" },
  );
  assert.deepEqual(resolveSourceFields("https://other.example/live.m3u8", []), {
    sourceURL: "https://other.example/live.m3u8",
    upstream: "",
    path: "",
  });
});
