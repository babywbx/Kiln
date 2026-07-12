import { h, icon, initials } from "/admin/assets/core/dom.js";
import { clearSession, endpoints, hasSession, loadSession, setUnauthorizedHandler } from "/admin/assets/core/api.js";
import { resetStore, startPolling, stopPolling, store, subscribe } from "/admin/assets/core/store.js";
import { SECTIONS, configureRouter, isDirty, navigate, registerRoute, renderRoute, startRouter } from "/admin/assets/core/router.js";
import { button, emptyState, pageHead } from "/admin/assets/ui/kit.js";
import { attachMenu, closeModal, confirmDialog, toast, toastError } from "/admin/assets/ui/overlay.js";
import { brandMark, renderLogin } from "/admin/assets/views/login.js";
import { renderOverview } from "/admin/assets/views/overview.js";
import { renderChannels } from "/admin/assets/views/channels.js";
import { renderChannelDetail } from "/admin/assets/views/channel-detail.js";
import { renderAccess } from "/admin/assets/views/access.js";
import { renderEgress } from "/admin/assets/views/egress.js";
import { renderSettings } from "/admin/assets/views/settings.js";

const NAV_ICONS = {
  overview: "layout-dashboard",
  channels: "tv",
  access: "key-round",
  egress: "network",
  settings: "settings",
};

const root = document.getElementById("root");
const compactMedia = matchMedia("(max-width: 1080px)");

let shell = null;

registerRoute("overview", renderOverview);
registerRoute("channels", (ctx) => (ctx.id ? renderChannelDetail(ctx) : renderChannels(ctx)));
registerRoute("access", renderAccess);
registerRoute("egress", renderEgress);
registerRoute("settings", renderSettings);

function setTheme(theme) {
  document.documentElement.dataset.theme = theme;
  localStorage.setItem("kiln.admin.theme", theme);
}

function initTheme() {
  const saved = localStorage.getItem("kiln.admin.theme");
  setTheme(saved === "light" || saved === "dark" ? saved : matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
}

function buildShell() {
  const nav = h(
    "nav",
    { class: "nav" },
    Object.entries(SECTIONS).map(([section, label]) =>
      h(
        "a",
        { class: "nav-item", href: `/admin/${section}`, "data-route": true, "data-nav": section },
        icon(NAV_ICONS[section], 20),
        h("span", { class: "nav-label", text: label }),
      ),
    ),
  );

  const instanceDot = h("span", { class: "dot" });
  const instanceState = h("strong", { text: "正在连接" });
  const instanceDetail = h("small", { text: "检查服务状态" });

  const collapse = h(
    "button",
    { class: "sidebar-toggle", type: "button" },
    icon("panel-left-close", 18),
    h("span", { class: "sidebar-toggle-label", text: "收起导航" }),
  );

  const sidebar = h(
    "aside",
    { class: "sidebar", "aria-label": "主导航" },
    h(
      "div",
      { class: "sidebar-head" },
      h("a", { class: "brand", href: "/admin/overview", "data-route": true }, brandMark(), h("span", { class: "brand-name", text: "Kiln" })),
      h("button", { class: "icon-button sidebar-close", type: "button", "aria-label": "关闭导航", onClick: closeNav }, icon("x", 18)),
    ),
    nav,
    h(
      "div",
      { class: "sidebar-foot" },
      h("div", { class: "instance" }, instanceDot, h("span", {}, instanceState, instanceDetail)),
      collapse,
    ),
  );

  const pageTitle = h("strong", { class: "topbar-title", text: "总览" });
  const avatar = h("span", { class: "avatar avatar-user", text: initials(store.me?.username) });
  const accountButton = h(
    "button",
    { class: "account", type: "button" },
    avatar,
    h("span", { class: "account-copy" }, h("strong", { text: store.me?.username || "admin" }), h("small", { text: "管理员" })),
    icon("chevron-down", 14),
  );

  const themeButton = h("button", { class: "icon-button", type: "button", "aria-label": "切换外观", title: "切换外观" }, icon("sun", 18), icon("moon", 18));
  themeButton.classList.add("theme-toggle");
  themeButton.addEventListener("click", () => setTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark"));

  const progress = h("div", { class: "progress", "aria-hidden": "true" });
  const topbar = h(
    "header",
    { class: "topbar" },
    h("button", { class: "icon-button nav-open", type: "button", "aria-label": "打开导航", onClick: openNav }, icon("menu", 20)),
    h("div", { class: "topbar-copy" }, h("span", { class: "topbar-eyebrow", text: "Kiln 管理" }), pageTitle),
    h("div", { class: "topbar-actions" }, themeButton, accountButton),
    progress,
  );

  const main = h("main", { id: "main-content", tabindex: "-1" });
  const scrim = h("div", { class: "scrim", hidden: true, onClick: closeNav });

  const node = h("div", { class: "shell" }, scrim, sidebar, h("div", { class: "column" }, topbar, main));

  attachMenu(accountButton, [{ label: "退出登录", icon: "log-out", tone: "danger", onSelect: () => logout(true) }]);

  collapse.addEventListener("click", () => {
    const compact = node.classList.toggle("is-compact");
    localStorage.setItem("kiln.admin.sidebar", compact ? "compact" : "full");
    syncCollapse();
  });

  const syncCollapse = () => {
    const compact = node.classList.contains("is-compact");
    const label = compact ? "展开导航" : "收起导航";
    collapse.setAttribute("aria-label", label);
    collapse.setAttribute("title", label);
    collapse.querySelector(".sidebar-toggle-label").textContent = label;
  };

  if (localStorage.getItem("kiln.admin.sidebar") === "compact") node.classList.add("is-compact");
  syncCollapse();

  const paintInstance = () => {
    instanceDot.className = `dot ${store.online ? "is-ok" : "is-bad"}`;
    instanceState.textContent = store.online ? "服务正常" : "服务无响应";
    const sessions = store.status?.sessions?.length || 0;
    instanceDetail.textContent = store.online ? (sessions ? `${sessions} 个活动会话` : "没有活动会话") : "正在重试连接";
  };
  subscribe(paintInstance);
  paintInstance();

  return { node, main, nav, pageTitle, scrim, progress };
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
  const label = SECTIONS[route.section] || "控制台";
  shell.pageTitle.textContent = label;
  document.title = `${label} · Kiln`;
}

async function onBeforeLeave() {
  if (!isDirty()) return true;
  return confirmDialog({
    title: "放弃未保存的更改？",
    description: "当前页面的修改尚未应用，离开后将会丢失。",
    confirmLabel: "放弃更改",
  });
}

async function showApp() {
  closeModal();
  shell = buildShell();
  root.replaceChildren(shell.node);
  configureRouter({ outlet: shell.main, onBeforeLeave, onAfterRender, onLoading: setLoading });

  if (location.pathname.replace(/\/+$/, "") === "/admin") history.replaceState({}, "", "/admin/overview");

  startPolling();
  try {
    await renderRoute();
  } catch (error) {
    showRouteError(error);
  }
}

function showRouteError(error) {
  toastError(error, "页面加载失败");
  shell?.main.replaceChildren(
    pageHead("无法加载页面", "请求失败，但你的会话和其他数据仍然保留。"),
    emptyState("加载失败", error?.message || "未知错误", button("重试", { kind: "primary", iconName: "refresh-cw", onClick: () => renderRoute().catch(showRouteError) })),
  );
}

function showLogin() {
  stopPolling();
  shell = null;
  closeModal();
  root.replaceChildren(renderLogin(showApp));
  document.title = "登录 · Kiln";
}

function logout(notify) {
  clearSession();
  resetStore();
  if (notify) toast("已退出登录");
  history.replaceState({}, "", "/admin");
  showLogin();
}

async function bootstrap() {
  initTheme();
  loadSession();
  startRouter();
  setUnauthorizedHandler(() => logout(false));
  compactMedia.addEventListener("change", closeNav);

  try {
    const root = await fetch("/", { headers: { Accept: "application/json" }, cache: "no-store" }).then((response) => response.json());
    store.version = root.version || "";
  } catch {
    /* the version string is decorative */
  }

  if (!hasSession()) return showLogin();

  try {
    store.me = await endpoints.me();
    await showApp();
  } catch {
    clearSession();
    showLogin();
  }
}

window.addEventListener("unhandledrejection", (event) => {
  if (event.reason?.name === "AbortError") event.preventDefault();
});

bootstrap();
