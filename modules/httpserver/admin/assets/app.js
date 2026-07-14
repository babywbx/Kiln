import { h, icon, initials } from "/admin/assets/core/dom.js";
import {
  clearSession,
  endpoints,
  hasSession,
  loadSession,
  requestSharedSession,
  setExternalLogoutHandler,
  setSessionAvailableHandler,
  setUnauthorizedHandler,
} from "/admin/assets/core/api.js";
import { i18n } from "/admin/assets/core/i18n.js";
import { resetStore, startPolling, stopPolling, store, subscribe } from "/admin/assets/core/store.js";
import { SECTIONS, configureRouter, isDirty, markDirty, navigate, registerRoute, renderRoute, startRouter } from "/admin/assets/core/router.js";
import { button, emptyState, pageHead } from "/admin/assets/ui/kit.js";
import { attachMenu, closeModal, confirmDialog, toast } from "/admin/assets/ui/overlay.js";
import { brandMark, renderLogin } from "/admin/assets/views/login.js";
import { renderOverview } from "/admin/assets/views/overview.js";
import { renderChannels } from "/admin/assets/views/channels.js";
import { renderChannelDetail } from "/admin/assets/views/channel-detail.js";
import { renderEPG } from "/admin/assets/views/epg.js";
import { renderAccess } from "/admin/assets/views/access.js";
import { renderEgress } from "/admin/assets/views/egress.js";
import { configureSettingsActions, openAccountSettings, openLanguageSettings, renderSettings } from "/admin/assets/views/settings.js";

const root = document.getElementById("root");
const skipLink = document.querySelector(".skip-link");
const compactMedia = matchMedia("(max-width: 1080px)");
const LOGOUT_EVENT_KEY = "kiln.admin.logout";
let shell = null;

registerRoute("overview", renderOverview);
registerRoute("channels", (ctx) => (ctx.id || ctx.query.get("new") === "1" ? renderChannelDetail(ctx) : renderChannels(ctx)));
registerRoute("epg", renderEPG);
registerRoute("access", renderAccess);
registerRoute("egress", renderEgress);
registerRoute("settings", renderSettings);

configureSettingsActions({
  onAccountUpdated: async () => {
    shell?.paintAccount();
    for (const label of document.querySelectorAll("[data-account-username]")) label.textContent = store.me?.username || "—";
    if (!isDirty()) await renderRoute().catch(showRouteError);
  },
  onLocaleChanged: async (locale) => {
    if (isDirty() && !(await onBeforeLeave())) return false;
    markDirty(false);
    i18n.setLocale(locale);
    await showApp();
    return true;
  },
});

function sectionLabel(section) {
  const key = `nav.${section}`;
  const translated = i18n.t(key);
  return translated === key ? SECTIONS[section]?.label || i18n.t("shell.console") : translated;
}

function readPreference(key) {
  try {
    return localStorage.getItem(key) || "";
  } catch {
    return "";
  }
}

function writePreference(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* preferences are optional */
  }
}

function setTheme(theme) {
  document.documentElement.dataset.theme = theme;
  writePreference("kiln.admin.theme", theme);
}

function initTheme() {
  const saved = readPreference("kiln.admin.theme");
  setTheme(saved === "light" || saved === "dark" ? saved : matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
}

function buildShell() {
  const nav = h(
    "nav",
    { class: "nav" },
    Object.entries(SECTIONS).map(([section, meta]) =>
      h(
        "a",
        { class: "nav-item", href: `/admin/${section}`, "data-route": true, "data-nav": section },
        icon(meta.icon, 20),
        h("span", { class: "nav-label", text: sectionLabel(section) }),
      ),
    ),
  );

  const instanceDot = h("span", { class: "dot" });
  const instanceState = h("strong", { text: i18n.t("shell.connecting") });
  const instanceDetail = h("small", { text: i18n.t("shell.checkingService") });

  const collapse = h(
    "button",
    { class: "sidebar-toggle", type: "button" },
    icon("panel-left-close", 18),
    h("span", { class: "sidebar-toggle-label", text: i18n.t("shell.collapseNavigation") }),
  );

  const sidebar = h(
    "aside",
    { class: "sidebar", "aria-label": i18n.t("shell.mainNavigation") },
    h(
      "div",
      { class: "sidebar-head" },
      h("a", { class: "brand", href: "/admin/overview", "data-route": true }, brandMark(), h("span", { class: "brand-name", text: "Kiln" })),
      h("button", { class: "icon-button sidebar-close", type: "button", "aria-label": i18n.t("shell.closeNavigation"), onClick: closeNav }, icon("x", 18)),
    ),
    nav,
    h(
      "div",
      { class: "sidebar-foot" },
      h("div", { class: "instance" }, instanceDot, h("span", {}, instanceState, instanceDetail)),
      collapse,
    ),
  );

  const pageTitle = h("strong", { class: "topbar-title", text: sectionLabel("overview") });
  const avatar = h("span", { class: "avatar avatar-user", text: initials(store.me?.username) });
  const accountName = h("strong", { text: store.me?.username || "admin" });
  const accountButton = h(
    "button",
    { class: "account", type: "button" },
    avatar,
    h("span", { class: "account-copy" }, accountName, h("small", { text: i18n.t("shell.adminRole") })),
    icon("chevron-down", 14),
  );

  const themeButton = h(
    "button",
    { class: "icon-button", type: "button", "aria-label": i18n.t("shell.switchAppearance"), title: i18n.t("shell.switchAppearance") },
    icon("sun", 18),
    icon("moon", 18),
  );
  themeButton.classList.add("theme-toggle");
  themeButton.addEventListener("click", () => setTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark"));

  const progress = h("div", { class: "progress", "aria-hidden": "true" });
  const topbar = h(
    "header",
    { class: "topbar" },
    h("button", { class: "icon-button nav-open", type: "button", "aria-label": i18n.t("shell.openNavigation"), onClick: openNav }, icon("menu", 20)),
    h("div", { class: "topbar-copy" }, h("span", { class: "topbar-eyebrow", text: i18n.t("shell.eyebrow") }), pageTitle),
    h("div", { class: "topbar-actions" }, themeButton, accountButton),
    progress,
  );

  const main = h("main", { id: "main-content", tabindex: "-1" });
  const scrim = h("div", { class: "scrim", hidden: true, onClick: closeNav });

  const node = h("div", { class: "shell" }, scrim, sidebar, h("div", { class: "column" }, topbar, main));

  const accountMenu = attachMenu(accountButton, [
    { label: i18n.t("shell.accountSettings"), icon: "user-round-cog", onSelect: openAccountSettings },
    { label: i18n.t("shell.language"), icon: "languages", onSelect: openLanguageSettings },
    { label: i18n.t("shell.signOut"), icon: "log-out", tone: "danger", onSelect: () => logout(true) },
  ]);

  collapse.addEventListener("click", () => {
    const compact = node.classList.toggle("is-compact");
    writePreference("kiln.admin.sidebar", compact ? "compact" : "full");
    syncCollapse();
  });

  const syncCollapse = () => {
    const compact = node.classList.contains("is-compact");
    const label = i18n.t(compact ? "shell.expandNavigation" : "shell.collapseNavigation");
    collapse.setAttribute("aria-label", label);
    collapse.setAttribute("title", label);
    collapse.querySelector(".sidebar-toggle-label").textContent = label;
  };

  if (readPreference("kiln.admin.sidebar") === "compact") node.classList.add("is-compact");
  syncCollapse();

  const paintInstance = () => {
    instanceDot.className = `dot ${store.online ? "is-ok" : "is-bad"}`;
    instanceState.textContent = i18n.t(store.online ? "shell.serviceAvailable" : "shell.serviceUnavailable");
    const sessions = store.status?.sessions?.length || 0;
    instanceDetail.textContent = store.online
      ? sessions
        ? i18n.t("shell.activeSessions", { count: sessions })
        : i18n.t("shell.noActiveSessions")
      : i18n.t("shell.retryingConnection");
  };
  const unsubscribe = subscribe(paintInstance);
  paintInstance();

  const dispose = () => {
    unsubscribe();
    accountMenu.dispose();
  };

  const paintAccount = () => {
    const username = store.me?.username || "admin";
    avatar.textContent = initials(username);
    accountName.textContent = username;
  };

  return { node, main, nav, pageTitle, scrim, progress, dispose, paintAccount };
}

function setLoading(active) {
  shell?.progress.classList.toggle("is-active", active);
  shell?.main.setAttribute("aria-busy", String(active));
}

function openNav() {
  shell?.node.classList.add("is-nav-open");
  if (shell) shell.scrim.hidden = false;
}

function closeNav() {
  shell?.node.classList.remove("is-nav-open");
  if (shell) shell.scrim.hidden = true;
}

function onAfterRender(route) {
  closeNav();
  if (!shell) return;
  for (const link of shell.nav.querySelectorAll("[data-nav]")) {
    if (link.dataset.nav === route.section) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
  const label = sectionLabel(route.section);
  shell.pageTitle.textContent = label;
  document.title = i18n.t("meta.consoleTitle", { page: label });
}

async function onBeforeLeave() {
  if (!isDirty()) return true;
  return confirmDialog({
    title: i18n.t("shell.discardTitle"),
    description: i18n.t("shell.discardDescription"),
    confirmLabel: i18n.t("shell.discardAction"),
  });
}

async function showApp() {
  shell?.dispose();
  shell = null;
  closeModal();
  document.documentElement.lang = i18n.locale;
  skipLink.textContent = i18n.t("shared.skipToContent");
  shell = buildShell();
  root.replaceChildren(shell.node);
  configureRouter({
    outlet: shell.main,
    onBeforeLeave,
    onAfterRender,
    onLoading: setLoading,
    onError: showRouteError,
    onUnauthenticated: showLogin,
    isAuthenticated: hasSession,
  });

  startPolling();
  try {
    await renderRoute();
  } catch (error) {
    showRouteError(error);
  }
}

function showRouteError(error) {
  const detail = error?.requestId ? i18n.t("shared.requestId", { id: error.requestId }) : i18n.t("error.tryAgain");
  toast(i18n.t("error.pageLoadTitle"), detail, "danger");
  shell?.main.replaceChildren(
    pageHead(i18n.t("error.pageUnavailable"), i18n.t("error.pageDescription")),
    emptyState(
      i18n.t("error.loadFailed"),
      detail,
      button(i18n.t("shared.retry"), { kind: "primary", iconName: "refresh-cw", onClick: () => renderRoute().catch(showRouteError) }),
    ),
  );
}

function showLogin() {
  stopPolling();
  shell?.dispose();
  shell = null;
  closeModal();
  paintLoginChrome();
  root.replaceChildren(renderLogin(showApp, { i18n, onLocaleChange: paintLoginChrome }));
}

function paintLoginChrome() {
  document.documentElement.lang = i18n.locale;
  skipLink.textContent = i18n.t("shared.skipToContent");
  document.title = i18n.t("meta.loginTitle");
}

function finishLogout(notify, preserveRoute, broadcast) {
  markDirty(false);
  clearSession({ broadcast });
  resetStore();
  if (broadcast) {
    try {
      localStorage.setItem(LOGOUT_EVENT_KEY, String(Date.now()));
      localStorage.removeItem(LOGOUT_EVENT_KEY);
    } catch {
      /* storage may be unavailable in privacy-restricted browsers */
    }
  }
  if (notify) toast(i18n.t("shell.signedOut"));
  if (!preserveRoute) history.replaceState({}, "", "/admin");
  showLogin();
}

async function logout(notify, { preserveRoute = false, confirm = true, broadcast = true } = {}) {
  if (confirm && !(await onBeforeLeave())) return;
  finishLogout(notify, preserveRoute, broadcast);
}

async function bootstrap() {
  initTheme();
  loadSession();
  startRouter();
  setUnauthorizedHandler(() => logout(false, { preserveRoute: true, confirm: false, broadcast: false }));
  setExternalLogoutHandler(() => finishLogout(false, true, false));
  setSessionAvailableHandler(() => {
    if (!shell) location.reload();
  });
  compactMedia.addEventListener("change", closeNav);
  window.addEventListener("storage", (event) => {
    if (event.key === LOGOUT_EVENT_KEY) finishLogout(false, true, false);
  });

  try {
    const root = await fetch("/", { headers: { Accept: "application/json" }, cache: "no-store" }).then((response) => response.json());
    store.version = root.version || "";
  } catch {
    /* the version string is decorative */
  }

  if (!hasSession()) {
    await requestSharedSession();
    if (!hasSession()) return showLogin();
  }

  try {
    store.me = await endpoints.me();
    if (store.me.role !== "admin") {
      clearSession({ broadcast: false });
      resetStore();
      return showLogin();
    }
    await showApp();
  } catch (error) {
    if (error?.status === 401 || error?.status === 403) clearSession({ broadcast: false });
    showLogin();
  }
}

window.addEventListener("unhandledrejection", (event) => {
  if (event.reason?.name === "AbortError") event.preventDefault();
});

bootstrap();
