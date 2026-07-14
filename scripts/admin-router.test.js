import assert from "node:assert/strict";
import test from "node:test";

import { resolveAdminRoute } from "../modules/httpserver/admin/assets/core/route-model.js";

const sections = new Set(["overview", "channels", "epg", "access", "egress", "settings"]);

test("admin routes canonicalize roots and preserve valid deep links", () => {
  assert.deepEqual(resolveAdminRoute("/admin", "", sections), {
    section: "overview", id: "", query: "", url: "/admin/overview",
  });
  assert.deepEqual(resolveAdminRoute("/admin/", "", sections), {
    section: "overview", id: "", query: "", url: "/admin/overview",
  });
  assert.deepEqual(resolveAdminRoute("/admin/channels/news%20hd/", "?from=list", sections), {
    section: "channels", id: "news hd", query: "?from=list", url: "/admin/channels/news%20hd?from=list",
  });
});

test("admin routes reject unknown sections and extra path segments", () => {
  for (const pathname of [
    "/not-admin",
    "/admin/not-a-route",
    "/admin/overview/extra",
    "/admin/settings/extra",
    "/admin/channels/news/extra",
    "/admin/channels/%E0%A4%A",
  ]) {
    assert.equal(resolveAdminRoute(pathname, "?ignored=true", sections).url, "/admin/overview", pathname);
  }
});

test("channel creation uses query state outside the channel id namespace", () => {
  const route = resolveAdminRoute("/admin/channels", "?new=1", sections);
  assert.equal(route.section, "channels");
  assert.equal(route.id, "");
  assert.equal(route.url, "/admin/channels?new=1");

  const detail = resolveAdminRoute("/admin/channels/news", "?new=1", sections);
  assert.equal(detail.id, "news");
  assert.equal(detail.query, "");
  assert.equal(detail.url, "/admin/channels/news");
});
