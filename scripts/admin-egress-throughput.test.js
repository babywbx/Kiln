import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const assetsRoot = path.resolve("modules/httpserver/admin/assets");
const status = await readFile(path.join(assetsRoot, "core/egress-status.js"), "utf8");
const view = await readFile(path.join(assetsRoot, "views/egress.js"), "utf8");
const i18n = await readFile(path.join(assetsRoot, "core/i18n.js"), "utf8");
const probe = await readFile("modules/httpserver/egress_probe.go", "utf8");

test("a reachable but throttled path is reported as a failure, not a success", () => {
  assert.match(status, /slow: "egress\.outcome\.slow"/, "the slow verdict needs its own message");
  assert.equal((i18n.match(/"egress\.outcome\.slow"/g) || []).length, 3, "every locale must carry the verdict");
});

test("the operator sees the measured rate, not just a green tick", () => {
  assert.match(status, /export function egressThroughputLabel/);
  assert.match(view, /egressThroughputLabel\(result\)/, "the success notice must carry the number");
  assert.equal((i18n.match(/"egress\.result\.throughput"/g) || []).length, 3);
});

test("the probe refuses to judge a response too small to measure", () => {
  assert.match(probe, /probeSampleMinimum\s*=\s*256\s*<<\s*10/, "a small page cannot prove a path carries media");
  assert.match(probe, /sample\.bytes >= probeSampleMinimum/);
  assert.match(probe, /probeSampleBytes\s*=\s*4\s*<<\s*20/, "the sample must stay bounded");
});

test("the verdict threshold is configurable and can be turned off", () => {
  assert.match(probe, /case configured < 0:\n\t\treturn 0/, "a negative floor disables the verdict");
  assert.match(probe, /case configured == 0:\n\t\treturn probeDefaultFloor/);
});
