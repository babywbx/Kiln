export function isValidSourceURL(value) {
  const raw = String(value || "").trim();
  if (!raw) return false;
  try {
    const url = new URL(raw);
    return (url.protocol === "http:" || url.protocol === "https:") && Boolean(url.host) && !url.hash;
  } catch {
    return false;
  }
}

export function resolveSourceFields(value, upstreams = []) {
  const raw = String(value || "").trim();
  let target;
  try {
    target = new URL(raw);
  } catch {
    return { sourceURL: raw, upstream: "", path: "" };
  }
  const matches = [];
  for (const upstream of upstreams) {
    try {
      const base = new URL(upstream.base_url);
      const basePath = base.pathname.replace(/\/+$/, "");
      if (target.origin === base.origin && target.username === base.username && target.password === base.password && !base.search && !base.hash &&
          (target.pathname === basePath || target.pathname.startsWith(`${basePath}/`))) {
        matches.push({ upstream, basePath });
      }
    } catch {
      /* invalid configured upstreams are ignored and rejected by the server */
    }
  }
  matches.sort((left, right) => right.basePath.length - left.basePath.length);
  const match = matches[0];
  if (!match) return { sourceURL: raw, upstream: "", path: "" };
  const relativePath = target.pathname.slice(match.basePath.length) || "/";
  return {
    sourceURL: "",
    upstream: match.upstream.id,
    path: `${relativePath.startsWith("/") ? "" : "/"}${relativePath}${target.search}`,
  };
}
