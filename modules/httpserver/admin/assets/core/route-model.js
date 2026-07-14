const DEFAULT_SECTION = "overview";

function fallbackRoute() {
  return { section: DEFAULT_SECTION, id: "", query: "", url: `/admin/${DEFAULT_SECTION}` };
}

export function resolveAdminRoute(pathname, search, sections) {
  const trimmed = pathname.replace(/\/+$/, "") || "/";
  if (trimmed === "/admin") return fallbackRoute();

  const segments = trimmed.split("/").filter(Boolean);
  if (segments[0] !== "admin" || segments.length < 2 || segments.length > 3) return fallbackRoute();

  const section = segments[1];
  if (!sections.has(section)) return fallbackRoute();
  if (section !== "channels" && segments.length !== 2) return fallbackRoute();

  let id = "";
  if (segments.length === 3) {
    try {
      id = decodeURIComponent(segments[2]);
    } catch {
      return fallbackRoute();
    }
    if (!id || id.includes("/") || id === "." || id === "..") return fallbackRoute();
  }

  let query = typeof search === "string" && search.startsWith("?") ? search : "";
  if (id && query) {
    const params = new URLSearchParams(query);
    params.delete("new");
    query = params.size ? `?${params}` : "";
  }
  const suffix = id ? `/${encodeURIComponent(id)}` : "";
  return { section, id, query, url: `/admin/${section}${suffix}${query}` };
}
