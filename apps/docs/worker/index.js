const YEAR = 60 * 60 * 24 * 365;
const VARY = "Accept-Language, Cookie, Sec-Fetch-Site";

function storedLanguage(request) {
  const cookie = request.headers.get("cookie");
  const match = cookie && cookie.match(/(?:^|;\s*)lang=(en|zh)(?:;|$)/);
  return match ? match[1] : null;
}

function prefersEnglish(header) {
  if (!header) return false;
  let zh = -1;
  let en = -1;
  for (const entry of header.split(",")) {
    const [tag, ...params] = entry.split(";");
    const quality = params.find((p) => p.trim().startsWith("q="));
    const weight = quality ? Number(quality.trim().slice(2)) : 1;
    if (Number.isNaN(weight)) continue;
    const lang = tag.trim().toLowerCase();
    if (lang.startsWith("zh") && weight > zh) zh = weight;
    if ((lang.startsWith("en") || lang === "*") && weight > en) en = weight;
  }
  return en > zh;
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const root = url.pathname === "/";

    if (root && request.headers.get("sec-fetch-site") !== "same-origin") {
      const language = storedLanguage(request) ?? (prefersEnglish(request.headers.get("accept-language")) ? "en" : "zh");
      if (language === "en") {
        return new Response(null, {
          status: 302,
          headers: { location: "/en/", vary: VARY, "cache-control": "no-store" },
        });
      }
    }

    const response = await env.ASSETS.fetch(request);
    if (response.status !== 200) return response;

    const headers = new Headers(response.headers);
    headers.append("set-cookie", `lang=${root ? "zh" : "en"}; Path=/; Max-Age=${YEAR}; SameSite=Lax`);
    headers.set("vary", VARY);
    return new Response(response.body, { status: response.status, headers });
  },
};
