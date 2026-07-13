import { endpoints, hasSession, isAbort } from "/admin/assets/core/api.js";

const POLL_INTERVAL_MS = 1000;

export const store = {
  me: null,
  version: "",
  status: null,
  online: false,
  lastSyncAt: 0,
  channels: [],
  upstreams: [],
};

const listeners = new Set();
let timer = null;
let inFlight = null;
let catalogPromise = null;

export function subscribe(listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function emit() {
  for (const listener of listeners) listener(store);
}

export function sessionFor(channelID) {
  return (store.status?.sessions || []).find((item) => item.channel_id === channelID) || null;
}

export function upstreamBase(id) {
  return store.upstreams.find((item) => item.id === id)?.base_url || "";
}

export function sourceURL(upstreamID, path) {
  const base = upstreamBase(upstreamID);
  if (!base) return path || "尚未选择来源服务器";
  return `${base.replace(/\/+$/, "")}/${String(path || "").replace(/^\/+/, "")}`;
}

export async function refreshStatus() {
  if (!hasSession() || inFlight) return;
  inFlight = endpoints.status();
  try {
    store.status = await inFlight;
    store.online = true;
    store.lastSyncAt = Date.now();
  } catch (error) {
    if (isAbort(error)) return;
    store.online = false;
  } finally {
    inFlight = null;
    emit();
  }
}

// Poll only while the tab is visible; a hidden tab must not keep the upstream busy.
function schedule() {
  clearTimeout(timer);
  if (!hasSession() || document.hidden) return;
  timer = setTimeout(async () => {
    await refreshStatus();
    schedule();
  }, POLL_INTERVAL_MS);
}

export function startPolling() {
  stopPolling();
  refreshStatus().then(schedule);
}

export function stopPolling() {
  clearTimeout(timer);
  timer = null;
}

document.addEventListener("visibilitychange", () => {
  if (document.hidden) stopPolling();
  else if (hasSession()) startPolling();
});

export async function loadCatalog({ force = false, signal } = {}) {
  if (!force && store.channels.length && store.upstreams.length) return store;
  if (!force && catalogPromise) return catalogPromise;

  catalogPromise = (async () => {
    const [channels, upstreams] = await Promise.all([endpoints.channels(signal), endpoints.upstreams(signal)]);
    store.channels = [...(channels.channels || [])].sort((a, b) => (a.sort_order || 0) - (b.sort_order || 0));
    store.upstreams = upstreams.upstreams || [];
    emit();
    return store;
  })();

  try {
    return await catalogPromise;
  } finally {
    catalogPromise = null;
  }
}

export function invalidateCatalog() {
  store.channels = [];
  store.upstreams = [];
}

export function resetStore() {
  stopPolling();
  store.me = null;
  store.status = null;
  store.online = false;
  store.channels = [];
  store.upstreams = [];
  emit();
}
