import { subscribe } from "/admin/assets/core/store.js";
import { resolveAdminRoute } from "/admin/assets/core/route-model.js";

export const SECTIONS = {
  overview: { icon: "layout-dashboard" },
  channels: { icon: "tv" },
  epg: { icon: "list" },
  access: { icon: "key-round" },
  egress: { icon: "network" },
  settings: { icon: "settings" },
};

const routes = new Map();
let outlet = null;
let onBeforeLeave = async () => true;
let onAfterRender = () => {};
let onLoading = () => {};
let onError = () => {};
let onUnauthenticated = () => {};
let isAuthenticated = () => true;

// Every navigation gets a fresh epoch. Async work started by a previous view
// checks ctx.alive() before touching the DOM, so a late poll or fetch can never
// paint over the page the user is actually looking on.
let epoch = 0;
let disposers = [];
let currentURL = location.pathname + location.search;
let dirty = false;

export function registerRoute(section, render) {
  routes.set(section, render);
}

export function configureRouter(options) {
  outlet = options.outlet;
  onBeforeLeave = options.onBeforeLeave || onBeforeLeave;
  onAfterRender = options.onAfterRender || onAfterRender;
  onLoading = options.onLoading || onLoading;
  onError = options.onError || onError;
  onUnauthenticated = options.onUnauthenticated || onUnauthenticated;
  isAuthenticated = options.isAuthenticated || isAuthenticated;
}

export function currentRoute() {
  const resolved = resolveAdminRoute(location.pathname, location.search, new Set(routes.keys()));
  return {
    ...resolved,
    query: new URLSearchParams(resolved.query),
  };
}

export function markDirty(value = true) {
  dirty = value;
}

export function isDirty() {
  return dirty;
}

export async function navigate(path, { replace = false } = {}) {
  if (path === currentURL) return;
  if (!(await onBeforeLeave())) return;
  dirty = false;
  if (replace) history.replaceState({}, "", path);
  else history.pushState({}, "", path);
  try {
    await renderRoute();
  } catch (error) {
    onError(error);
  }
}

function teardown() {
  for (const dispose of disposers) {
    try {
      dispose();
    } catch {
      /* a failing disposer must not block the next view */
    }
  }
  disposers = [];
}

export async function renderRoute() {
  if (!isAuthenticated()) {
    onUnauthenticated();
    return;
  }
  epoch += 1;
  const localEpoch = epoch;
  teardown();

  const controller = new AbortController();
  disposers.push(() => controller.abort());

  const route = currentRoute();
  if (location.pathname + location.search !== route.url) history.replaceState({}, "", route.url);
  currentURL = route.url;

  const ctx = {
    ...route,
    signal: controller.signal,
    alive: () => epoch === localEpoch,
    navigate,
    markDirty,
    onDispose: (fn) => disposers.push(fn),
    watchStatus: (fn) => disposers.push(subscribe(fn)),
    reload: () => (epoch === localEpoch ? renderRoute() : Promise.resolve()),
  };

  onAfterRender(route);
  const render = routes.get(route.section);
  await mount(render, ctx, localEpoch);
}

// The outgoing view stays on screen until the next one is ready, so navigation
// never flashes an empty page; the progress bar carries the "working" signal.
async function mount(render, ctx, localEpoch) {
  onLoading(true);
  try {
    const view = await render(ctx);
    if (epoch !== localEpoch) return;
    outlet.replaceChildren(view);
    outlet.scrollTop = 0;
    window.scrollTo({ top: 0 });
  } catch (error) {
    if (epoch !== localEpoch || error?.name === "AbortError") return;
    throw error;
  } finally {
    if (epoch === localEpoch) onLoading(false);
  }
}

export function startRouter() {
  document.addEventListener("click", (event) => {
    const link = event.target.closest("a[data-route]");
    if (!link || event.defaultPrevented) return;
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    navigate(link.getAttribute("href")).catch(onError);
  });

  window.addEventListener("popstate", async () => {
    if (dirty && !(await onBeforeLeave())) {
      history.pushState({}, "", currentURL);
      return;
    }
    dirty = false;
    try {
      await renderRoute();
    } catch (error) {
      onError(error);
    }
  });

  window.addEventListener("beforeunload", (event) => {
    if (!dirty) return;
    event.preventDefault();
    event.returnValue = "";
  });
}
