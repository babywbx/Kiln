import { i18n } from "./i18n.js";

const TOKEN_KEY = "kiln.admin.token";
const EXPIRES_KEY = "kiln.admin.expires";
const ISSUED_KEY = "kiln.admin.issued";

export class ApiError extends Error {
  constructor(message, details = {}) {
    super(message);
    this.name = "ApiError";
    this.code = details.code || "http_error";
    this.status = details.status || 0;
    this.requestId = details.requestId || "";
  }

  get detail() {
    return this.requestId ? i18n.t("shared.requestId", { id: this.requestId }) : "";
  }
}

const session = { token: "", expiresAt: "", issuedAt: 0, authenticated: false, remember: false };
let onUnauthorized = () => {};
let onSessionAvailable = () => {};
let onExternalLogout = () => {};
let sessionGeneration = 0;
let lastSignOutAt = 0;
const sessionWaiters = new Map();
const sessionChannel = typeof window !== "undefined" && "BroadcastChannel" in window ? new BroadcastChannel("kiln.admin.session") : null;

function browserStorage(name) {
  try {
    return window[name];
  } catch {
    return null;
  }
}

function readStorage(storage, key) {
  try {
    return storage?.getItem(key) || "";
  } catch {
    return "";
  }
}

function writeStorage(storage, key, value) {
  try {
    storage?.setItem(key, value);
  } catch {
    /* storage may be unavailable in privacy-restricted browsers */
  }
}

function removeStorage(storage, key) {
  try {
    storage?.removeItem(key);
  } catch {
    /* storage may be unavailable in privacy-restricted browsers */
  }
}

export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn;
}

export function setSessionAvailableHandler(fn) {
  onSessionAvailable = fn;
}

export function setExternalLogoutHandler(fn) {
  onExternalLogout = fn;
}

function resetSession() {
  session.token = "";
  session.expiresAt = "";
  session.issuedAt = 0;
  session.authenticated = false;
  session.remember = false;
  for (const storage of [browserStorage("localStorage"), browserStorage("sessionStorage")]) {
    removeStorage(storage, TOKEN_KEY);
    removeStorage(storage, EXPIRES_KEY);
    removeStorage(storage, ISSUED_KEY);
  }
}

function cancelSessionWaiters() {
  for (const waiter of sessionWaiters.values()) {
    clearTimeout(waiter.initialTimer);
    clearTimeout(waiter.lateTimer);
    clearTimeout(waiter.cleanupTimer);
    if (!waiter.settled) {
      waiter.settled = true;
      waiter.resolve(false);
    }
  }
  sessionWaiters.clear();
}

function adoptSharedSession(message) {
  if (!message.token) return false;
  const issuedAt = Number(message.issuedAt || 0);
  if (!Number.isFinite(issuedAt) || issuedAt <= Math.max(lastSignOutAt, session.issuedAt)) return false;
  const expiresAt = Date.parse(message.expiresAt || "");
  if (Number.isFinite(expiresAt) && expiresAt <= Date.now()) return false;
  resetSession();
  const storage = browserStorage(message.remember ? "localStorage" : "sessionStorage");
  writeStorage(storage, TOKEN_KEY, message.token);
  writeStorage(storage, EXPIRES_KEY, message.expiresAt || "");
  writeStorage(storage, ISSUED_KEY, String(issuedAt));
  session.token = message.token;
  session.expiresAt = message.expiresAt || "";
  session.issuedAt = issuedAt;
  session.authenticated = true;
  session.remember = Boolean(message.remember);
  return true;
}

sessionChannel?.addEventListener("message", (event) => {
  const message = event.data || {};
  if (message.type === "request" && session.token) {
    sessionChannel.postMessage({
      type: "session",
      requestId: message.requestId,
      token: session.token,
      expiresAt: session.expiresAt,
      issuedAt: session.issuedAt,
      remember: session.remember,
    });
    return;
  }
  if (message.type === "session" && sessionWaiters.has(message.requestId)) {
    const waiter = sessionWaiters.get(message.requestId);
    waiter.candidates.push(message);
    if (waiter.completed && !waiter.lateTimer) {
      waiter.lateTimer = setTimeout(async () => {
        clearTimeout(waiter.cleanupTimer);
        sessionWaiters.delete(message.requestId);
        if (!session.authenticated && await adoptNewestSession(waiter.candidates, waiter.generation)) onSessionAvailable();
      }, 75);
    }
    return;
  }
  if (message.type === "signed-in") {
    const generation = sessionGeneration;
    validateAndAdoptSharedSession(message, generation).then((adopted) => {
      if (adopted) onSessionAvailable();
    });
  }
  if (message.type === "signed-out") {
    const signedOutAt = Number(message.issuedAt || Date.now());
    if (Number.isFinite(signedOutAt) && signedOutAt < session.issuedAt) return;
    lastSignOutAt = Math.max(lastSignOutAt, signedOutAt);
    sessionGeneration += 1;
    cancelSessionWaiters();
    resetSession();
    onExternalLogout();
  }
});

function activeStorage() {
  const tab = browserStorage("sessionStorage");
  return readStorage(tab, TOKEN_KEY) ? tab : browserStorage("localStorage");
}

export function loadSession() {
  const storage = activeStorage();
  const persistentStorage = browserStorage("localStorage");
  const token = readStorage(storage, TOKEN_KEY);
  const expiresAt = Date.parse(readStorage(storage, EXPIRES_KEY));
  const storedIssuedAt = Number(readStorage(storage, ISSUED_KEY));
  if (token && Number.isFinite(expiresAt) && expiresAt <= Date.now()) {
    clearSession({ broadcast: false });
    return "";
  }
  session.token = token;
  session.expiresAt = Number.isFinite(expiresAt) ? new Date(expiresAt).toISOString() : "";
  session.issuedAt = Number.isFinite(storedIssuedAt) && storedIssuedAt > 0 ? storedIssuedAt : tokenIssuedAt(token) * 1000;
  session.authenticated = Boolean(token);
  session.remember = Boolean(token && storage === persistentStorage);
  return token;
}

export function saveSession(token, expiresAt, remember) {
  const issuedAt = nextEventTime();
  sessionGeneration += 1;
  cancelSessionWaiters();
  resetSession();
  const storage = browserStorage(remember ? "localStorage" : "sessionStorage");
  writeStorage(storage, TOKEN_KEY, token);
  writeStorage(storage, EXPIRES_KEY, expiresAt || "");
  writeStorage(storage, ISSUED_KEY, String(issuedAt));
  session.token = token;
  session.expiresAt = expiresAt || "";
  session.issuedAt = issuedAt;
  session.authenticated = true;
  session.remember = remember;
  sessionChannel?.postMessage({ type: "signed-in", token, expiresAt: expiresAt || "", issuedAt, remember });
}

export function clearSession({ broadcast = true } = {}) {
  const issuedAt = nextEventTime();
  lastSignOutAt = issuedAt;
  sessionGeneration += 1;
  cancelSessionWaiters();
  resetSession();
  if (broadcast) sessionChannel?.postMessage({ type: "signed-out", issuedAt });
}

export function hasSession() {
  return session.authenticated;
}

export function remembersSession() {
  return Boolean(readStorage(browserStorage("localStorage"), TOKEN_KEY));
}

export function requestSharedSession(timeoutMs = 200) {
  if (session.authenticated || !sessionChannel) return Promise.resolve(session.authenticated);
  const requestId = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random()}`;
  return new Promise((resolve) => {
    const waiter = {
      candidates: [], completed: false, settled: false, resolve,
      generation: sessionGeneration,
      initialTimer: null, lateTimer: null, cleanupTimer: null,
    };
    waiter.initialTimer = setTimeout(async () => {
      if (waiter.candidates.length) {
        sessionWaiters.delete(requestId);
        waiter.settled = true;
        resolve(await adoptNewestSession(waiter.candidates, waiter.generation));
        return;
      }
      waiter.completed = true;
      waiter.cleanupTimer = setTimeout(() => sessionWaiters.delete(requestId), 10_000);
      waiter.settled = true;
      resolve(false);
    }, timeoutMs);
    sessionWaiters.set(requestId, waiter);
    sessionChannel.postMessage({ type: "request", requestId });
  });
}

async function adoptNewestSession(candidates, expectedGeneration) {
  candidates.sort((left, right) => Number(right.issuedAt || 0) - Number(left.issuedAt || 0));
  for (const candidate of candidates) {
    if (await validateAndAdoptSharedSession(candidate, expectedGeneration)) return true;
  }
  return false;
}

async function validateAndAdoptSharedSession(candidate, expectedGeneration) {
  if (expectedGeneration !== sessionGeneration) return false;
  if (!candidate.token) return false;
  const expiresAt = Date.parse(candidate.expiresAt || "");
  if (Number.isFinite(expiresAt) && expiresAt <= Date.now()) return false;
  try {
    const response = await fetch("/v1/me", {
      headers: { Authorization: `Bearer ${candidate.token}` },
      cache: "no-store",
    });
    if (!response.ok) return false;
    const account = await response.json();
    if (account?.role !== "admin") return false;
    if (expectedGeneration !== sessionGeneration) return false;
    return adoptSharedSession(candidate);
  } catch {
    return false;
  }
}

function tokenIssuedAt(token) {
  try {
    const payload = token.split(".")[1].replaceAll("-", "+").replaceAll("_", "/");
    const padded = payload.padEnd(Math.ceil(payload.length / 4) * 4, "=");
    return Number(JSON.parse(atob(padded)).iat || 0);
  } catch {
    return 0;
  }
}

function nextEventTime() {
  return Math.max(Date.now(), session.issuedAt + 1, lastSignOutAt + 1);
}

const RETRY_DELAYS_MS = [200, 600];
const RETRY_STATUSES = new Set([502, 503, 504]);

function retryable(method, signal) {
  if (signal?.aborted) return false;
  const verb = (method || "GET").toUpperCase();
  return verb === "GET" || verb === "HEAD";
}

async function fetchWithRetry(path, init, signal) {
  for (let attempt = 0; ; attempt += 1) {
    try {
      const response = await fetch(path, init);
      if (!RETRY_STATUSES.has(response.status) || attempt >= RETRY_DELAYS_MS.length) return response;
      await response.body?.cancel();
    } catch (error) {
      if (error?.name === "AbortError" || attempt >= RETRY_DELAYS_MS.length) throw error;
    }
    await new Promise((resolve) => setTimeout(resolve, RETRY_DELAYS_MS[attempt]));
    if (signal?.aborted) throw signal.reason ?? new DOMException("Aborted", "AbortError");
  }
}

export async function api(path, options = {}) {
  const { suppressUnauthorized = false, rawText = false, ...fetchOptions } = options;
  const headers = new Headers(fetchOptions.headers || {});
  if (fetchOptions.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (session.token) headers.set("Authorization", `Bearer ${session.token}`);

  const init = { ...fetchOptions, headers, cache: "no-store" };
  const response = retryable(fetchOptions.method, fetchOptions.signal)
    ? await fetchWithRetry(path, init, fetchOptions.signal)
    : await fetch(path, init);
  const requestId = response.headers.get("X-Request-ID") || "";
  const text = await response.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }

  if (!response.ok) {
    if (response.status === 401 && !suppressUnauthorized) onUnauthorized();
    throw new ApiError(data?.error?.message || response.statusText || i18n.t("error.requestFailed"), {
      code: data?.error?.code,
      status: response.status,
      requestId,
    });
  }
  session.authenticated = true;
  return rawText ? text : data;
}

export function isAbort(error) {
  return error?.name === "AbortError";
}

export const endpoints = {
  login: (body) => api("/v1/auth/login", { method: "POST", body: JSON.stringify(body), suppressUnauthorized: true }),
  me: (signal) => api("/v1/me", { signal, suppressUnauthorized: true }),
  updateCredentials: (body) => api("/v1/me/credentials", { method: "PUT", body: JSON.stringify(body) }),
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
  probeSource: (body, signal) => api("/v1/admin/source-probes", { method: "POST", body: JSON.stringify(body), signal }),
  warmupChannel: (id) => api(`/v1/admin/channels/${encodeURIComponent(id)}/warmup`, { method: "POST", body: "{}" }),
  previewChannel: (id) => api(`/v1/admin/channels/${encodeURIComponent(id)}/preview`, { method: "POST", body: "{}" }),
  stopSession: (id) => api(`/v1/admin/sessions/${encodeURIComponent(id)}`, { method: "DELETE" }),
  upstreams: (signal) => api("/v1/admin/upstreams", { signal }),
  importM3U: (body) => api("/v1/admin/import/m3u", { method: "POST", body: JSON.stringify(body) }),
  exportM3U: () => api("/v1/admin/exports/m3u", { method: "POST", body: "{}", rawText: true }),

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

  adminAPITokens: (signal) => api("/v1/admin/api-tokens", { signal }),
  createAdminAPIToken: (body) => api("/v1/admin/api-tokens", { method: "POST", body: JSON.stringify(body) }),
  updateAdminAPIToken: (id, body, revision) =>
    api(`/v1/admin/api-tokens/${encodeURIComponent(id)}`, {
      method: "PUT",
      headers: { "If-Match": String(revision || 0) },
      body: JSON.stringify(body),
    }),
  rotateAdminAPIToken: (id, revision) =>
    api(`/v1/admin/api-tokens/${encodeURIComponent(id)}/rotate`, {
      method: "POST", headers: { "If-Match": String(revision || 0) }, body: "{}",
    }),
  revokeAdminAPIToken: (id, revision) =>
    api(`/v1/admin/api-tokens/${encodeURIComponent(id)}/revoke`, {
      method: "POST", headers: { "If-Match": String(revision || 0) }, body: "{}",
    }),
  deleteAdminAPIToken: (id, revision) =>
    api(`/v1/admin/api-tokens/${encodeURIComponent(id)}`, {
      method: "DELETE", headers: { "If-Match": String(revision || 0) },
    }),
  adminAPITokenLogs: (signal) => api("/v1/admin/api-token-logs", { signal }),

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
