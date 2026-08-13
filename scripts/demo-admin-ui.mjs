import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import path from "node:path";

const assetsRoot = path.resolve("modules/httpserver/admin/assets");
const port = Number(process.env.PORT) || 4173;

const types = {
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".webp": "image/webp",
  ".woff2": "font/woff2",
};

const sessions = [
  {
    channel_id: "tvb-news",
    state: "failed",
    mode: "dash",
    engine: "",
    pack_mode: "",
    last_error:
      'engine=native but the source cannot be served natively: fetch manifest: upstream request failed: Get "https://edge.example.com/__cl/slocalr43/__c/ott_C_hevc/__op/cenc_m/__f/index.mpd?sig=5241963c03e54b6021936e73c95378a4&ext_start_limit=1786593137&ts=1786593137": remote error: tls: handshake failure',
  },
  {
    channel_id: "news-24",
    state: "failed",
    mode: "hls",
    engine: "ffmpeg_copy",
    pack_mode: "dynamic_timeline",
    last_error: "upstream returned 403",
  },
  {
    channel_id: "demo-live",
    state: "running",
    mode: "hls",
    engine: "native_rewrite",
    pack_mode: "dynamic_timeline",
    last_error: "",
  },
];

const page = `<!doctype html>
<html lang="zh-Hans">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Kiln 控制台演示 · 会话错误</title>
  <link rel="icon" href="/admin/assets/icon.webp">
  <link rel="stylesheet" href="/admin/assets/app.css">
</head>
<body>
  <div class="column"><main id="main-content" tabindex="-1"></main></div>
  <dialog class="modal" id="modal"><div id="modal-content"></div></dialog>
  <div class="toast-region" id="toast-region" aria-live="polite" aria-atomic="false"></div>
  <script type="module">
    document.documentElement.dataset.theme = new URLSearchParams(location.search).get("theme") || "dark";
    localStorage.setItem("kiln.admin.locale", new URLSearchParams(location.search).get("lang") || "zh-Hans");
    const sessions = ${JSON.stringify(sessions)};
    const canned = {
      "/v1/status": () => ({ sessions, session_count: sessions.length, goroutines: 42 }),
      "/v1/admin/channels": () => ({ channels: sessions.map((s, i) => ({ id: s.channel_id, title: s.channel_id, sort_order: i })) }),
      "/v1/admin/upstreams": () => ({ upstreams: [] }),
      "/v1/admin/epg/sources": () => ({ sources: [], statuses: [] }),
      "/v1/admin/epg/matches": () => ({ matches: [] }),
    };
    window.fetch = async (url) => {
      const key = String(url).split("?")[0];
      const body = canned[key] ? canned[key]() : {};
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    };

    const { saveSession } = await import("/admin/assets/core/api.js");
    const { subscribe } = await import("/admin/assets/core/store.js");
    const { renderOverview } = await import("/admin/assets/views/overview.js");
    const payload = btoa(JSON.stringify({ iat: 1, label: "demo" })).replace(/=+$/, "");
    saveSession(\`header.\${payload}.signature\`, "2999-01-01T00:00:00Z", false);

    const ctx = {
      signal: new AbortController().signal,
      alive: () => true,
      navigate: () => {},
      markDirty: () => {},
      onDispose: () => {},
      watchStatus: subscribe,
      reload: () => Promise.resolve(),
    };
    document.getElementById("main-content").replaceChildren(await renderOverview(ctx));

    const { refreshStatus } = await import("/admin/assets/core/store.js");
    setInterval(refreshStatus, 3000);
  </script>
</body>
</html>
`;

createServer(async (request, response) => {
  const url = new URL(request.url, "http://localhost");
  if (url.pathname === "/" || url.pathname === "/admin" || url.pathname === "/admin/") {
    response.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
    response.end(page);
    return;
  }
  if (!url.pathname.startsWith("/admin/assets/")) {
    response.writeHead(404).end();
    return;
  }
  const file = path.join(assetsRoot, url.pathname.slice("/admin/assets/".length));
  if (!file.startsWith(assetsRoot)) {
    response.writeHead(403).end();
    return;
  }
  try {
    const body = await readFile(file);
    response.writeHead(200, { "Content-Type": types[path.extname(file)] || "application/octet-stream" });
    response.end(body);
  } catch {
    response.writeHead(404).end();
  }
}).listen(port, "127.0.0.1", () => {
  console.log(`Kiln admin UI demo: http://127.0.0.1:${port}/`);
});
