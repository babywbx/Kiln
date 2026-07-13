const TOKEN_KEY = "kiln.admin.token";
const EXPIRES_KEY = "kiln.admin.expires";

export class ApiError extends Error {
  constructor(message, details = {}) {
    super(message);
    this.name = "ApiError";
    this.code = details.code || "http_error";
    this.status = details.status || 0;
    this.requestId = details.requestId || "";
  }

  get detail() {
    return [this.code, this.requestId && `请求 ${this.requestId}`].filter(Boolean).join(" · ");
  }
}

const session = { token: "" };
let onUnauthorized = () => {};

export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn;
}

function activeStorage() {
  return sessionStorage.getItem(TOKEN_KEY) ? sessionStorage : localStorage;
}

export function loadSession() {
  const storage = activeStorage();
  const token = storage.getItem(TOKEN_KEY) || "";
  const expiresAt = Date.parse(storage.getItem(EXPIRES_KEY) || "");
  if (token && Number.isFinite(expiresAt) && expiresAt <= Date.now()) {
    clearSession();
    return "";
  }
  session.token = token;
  return token;
}

export function saveSession(token, expiresAt, remember) {
  clearSession();
  const storage = remember ? localStorage : sessionStorage;
  storage.setItem(TOKEN_KEY, token);
  storage.setItem(EXPIRES_KEY, expiresAt || "");
  session.token = token;
}

export function clearSession() {
  session.token = "";
  for (const storage of [localStorage, sessionStorage]) {
    storage.removeItem(TOKEN_KEY);
    storage.removeItem(EXPIRES_KEY);
  }
}

export function hasSession() {
  return Boolean(session.token);
}

export function remembersSession() {
  return Boolean(localStorage.getItem(TOKEN_KEY));
}

export async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (session.token) headers.set("Authorization", `Bearer ${session.token}`);

  const response = await fetch(path, { ...options, headers, cache: "no-store" });
  const requestId = response.headers.get("X-Request-ID") || "";
  const text = await response.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }

  if (!response.ok) {
    if (response.status === 401 && session.token) onUnauthorized();
    throw new ApiError(data?.error?.message || response.statusText || "请求失败", {
      code: data?.error?.code,
      status: response.status,
      requestId,
    });
  }
  return data;
}

export function isAbort(error) {
  return error?.name === "AbortError";
}

export const endpoints = {
  login: (body) => api("/v1/auth/login", { method: "POST", body: JSON.stringify(body) }),
  me: (signal) => api("/v1/me", { signal }),
  status: (signal) => api("/v1/status", { signal }),

  channels: (signal) => api("/v1/admin/channels", { signal }),
  channel: (id, signal) => api(`/v1/admin/channels/${encodeURIComponent(id)}`, { signal }),
  createChannel: (body) => api("/v1/admin/channels", { method: "POST", body: JSON.stringify(body) }),
  updateChannel: (id, body, revision) =>
    api(`/v1/admin/channels/${encodeURIComponent(id)}`, {
      method: "PUT",
      headers: revision ? { "If-Match": String(revision) } : {},
      body: JSON.stringify(body),
    }),
  deleteChannel: (id, revision) =>
    api(`/v1/admin/channels/${encodeURIComponent(id)}`, {
      method: "DELETE",
      headers: revision ? { "If-Match": String(revision) } : {},
    }),
  reorderChannels: (ids, revisions) =>
    api("/v1/admin/channels/reorder", { method: "PUT", body: JSON.stringify({ ids, revisions }) }),
  enableAllChannels: () => api("/v1/admin/channels/enable-all", { method: "POST", body: "{}" }),
  disableAllChannels: () => api("/v1/admin/channels/disable-all", { method: "POST", body: "{}" }),
  probeChannel: (id) => api(`/v1/admin/channels/${encodeURIComponent(id)}/probe`, { method: "POST", body: "{}" }),
  warmupChannel: (id) => api(`/v1/admin/channels/${encodeURIComponent(id)}/warmup`, { method: "POST", body: "{}" }),
  previewChannel: (id) => api(`/v1/admin/channels/${encodeURIComponent(id)}/preview`, { method: "POST", body: "{}" }),
  stopSession: (id) => api(`/v1/admin/sessions/${encodeURIComponent(id)}`, { method: "DELETE" }),
  upstreams: (signal) => api("/v1/admin/upstreams", { signal }),
  importM3U: (body) => api("/v1/admin/import/m3u", { method: "POST", body: JSON.stringify(body) }),

  tokens: (signal) => api("/v1/admin/access-tokens", { signal }),
  createToken: (body) => api("/v1/admin/access-tokens", { method: "POST", body: JSON.stringify(body) }),
  revokeToken: (id, revision) =>
    api(`/v1/admin/access-tokens/${encodeURIComponent(id)}/revoke`, {
      method: "POST",
      headers: { "If-Match": String(revision || 0) },
      body: "{}",
    }),
  deleteToken: (id, revision) =>
    api(`/v1/admin/access-tokens/${encodeURIComponent(id)}`, {
      method: "DELETE",
      headers: { "If-Match": String(revision || 0) },
    }),
  accessLogs: (limit, signal) => api(`/v1/admin/access-logs?limit=${limit}`, { signal }),
  clearAccessLogs: () => api("/v1/admin/access-logs", { method: "DELETE" }),

  egress: (signal) => api("/v1/admin/egress", { signal }),
  saveEgress: (body, revision) =>
    api("/v1/admin/egress", { method: "PUT", headers: { "If-Match": String(revision || 0) }, body: JSON.stringify(body) }),
  testEgress: (body) => api("/v1/admin/egress/test", { method: "POST", body: JSON.stringify(body) }),

  epgPresets: (signal) => api("/v1/admin/epg/presets", { signal }),
  epgSources: (signal) => api("/v1/admin/epg/sources", { signal }),
  epgMatches: (signal) => api("/v1/admin/epg/matches", { signal }),
  createEPGSource: (body) => api("/v1/admin/epg/sources", { method: "POST", body: JSON.stringify(body) }),
  updateEPGSource: (id, body, revision) =>
    api(`/v1/admin/epg/sources/${encodeURIComponent(id)}`, {
      method: "PUT",
      headers: { "If-Match": String(revision || 0) },
      body: JSON.stringify(body),
    }),
  deleteEPGSource: (id, revision) =>
    api(`/v1/admin/epg/sources/${encodeURIComponent(id)}`, {
      method: "DELETE",
      headers: { "If-Match": String(revision || 0) },
    }),
  refreshEPG: () => api("/v1/admin/epg/refresh", { method: "POST", body: "{}" }),

  settings: (signal) => api("/v1/admin/settings", { signal }),
  saveSettings: (body, revision) =>
    api("/v1/admin/settings", { method: "PUT", headers: { "If-Match": String(revision || 0) }, body: JSON.stringify(body) }),
};
