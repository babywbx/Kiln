import { buildSearchIndex, searchIndex } from "/admin/assets/search.js";

const $ = (id) => document.getElementById(id);
const app = $("app-shell");
const main = $("main-content");
const modal = $("modal");

const state = {
  token: "",
  me: null,
  version: "",
  channels: [],
  upstreams: [],
  searchIndex: null,
  pollTimer: null,
  routeAbort: null,
  dirtyForm: false,
  suppressNextPop: false,
};

class ApiError extends Error {
  constructor(message, details) {
    super(message);
    this.name = "ApiError";
    Object.assign(this, details);
  }
}

function h(tag, attributes = {}, ...children) {
  const element = document.createElement(tag);
  for (const [key, value] of Object.entries(attributes)) {
    if (value == null || value === false) continue;
    if (key === "class") element.className = value;
    else if (key === "text") element.textContent = String(value);
    else if (key === "htmlFor") element.htmlFor = value;
    else if (key === "dataset") Object.assign(element.dataset, value);
    else if (key === "style") Object.assign(element.style, value);
    else if (key.startsWith("on") && typeof value === "function") element.addEventListener(key.slice(2).toLowerCase(), value);
    else if (key in element && !key.startsWith("aria")) element[key] = value;
    else element.setAttribute(key, value === true ? "" : String(value));
  }
  const append = (child) => {
    if (child == null || child === false) return;
    if (Array.isArray(child)) child.forEach(append);
    else element.append(child instanceof Node ? child : document.createTextNode(String(child)));
  };
  children.forEach(append);
  return element;
}

function icon(text) {
  return h("span", { "aria-hidden": "true", text });
}

function button(label, options = {}) {
  const { kind = "secondary", small = false, onClick, type = "button", disabled = false, ariaLabel } = options;
  return h("button", {
    class: `button button-${kind}${small ? " button-small" : ""}`,
    type,
    disabled,
    "aria-label": ariaLabel,
    onClick,
  }, label);
}

function field(label, input, hint = "") {
  const id = input.id || `field-${crypto.randomUUID()}`;
  input.id = id;
  const children = [h("label", { htmlFor: id, text: label }), input];
  if (hint) children.push(h("p", { class: "field-hint", text: hint }));
  return h("div", { class: "field" }, children);
}

function formatNumber(value) {
  return new Intl.NumberFormat("zh-Hans").format(Number(value || 0));
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let amount = bytes;
  let unit = -1;
  do { amount /= 1024; unit += 1; } while (amount >= 1024 && unit < units.length - 1);
  return `${amount >= 10 ? amount.toFixed(0) : amount.toFixed(1)} ${units[unit]}`;
}

function formatDuration(seconds) {
  const value = Number(seconds || 0);
  const days = Math.floor(value / 86400);
  const hours = Math.floor((value % 86400) / 3600);
  const minutes = Math.floor((value % 3600) / 60);
  if (days) return `${days}天 ${hours}小时`;
  if (hours) return `${hours}小时 ${minutes}分`;
  if (minutes) return `${minutes}分 ${value % 60}秒`;
  return `${value}秒`;
}

function formatTime(seconds) {
  if (!seconds) return "从未";
  return new Intl.DateTimeFormat("zh-Hans", { dateStyle: "medium", timeStyle: "short" }).format(new Date(Number(seconds) * 1000));
}

function safeProxyURL(raw) {
  try {
    const value = new URL(raw);
    return `${value.protocol}//${value.host}`;
  } catch {
    return "地址无效";
  }
}

function proxyHasCredentials(proxy) {
  if (proxy.credential_configured) return true;
  try { return Boolean(new URL(proxy.url).username); } catch { return false; }
}

function badge(label, tone = "") {
  return h("span", { class: `badge ${tone}`.trim(), text: label });
}

function statusBadge(status) {
  const labels = { running: ["运行中", "success"], starting: ["正在启动", "warning"], restarting: ["正在重启", "warning"], failed: ["失败", "danger"] };
  const [label, tone] = labels[status] || [status || "未知", ""];
  return badge(label, tone);
}

function toast(title, message = "", tone = "") {
  const item = h("div", { class: `toast ${tone}`, role: tone === "danger" ? "alert" : "status" },
    h("span", { text: tone === "danger" ? "!" : "✓", "aria-hidden": "true" }),
    h("div", {}, h("strong", { text: title }), message ? h("p", { text: message }) : null),
  );
  $("toast-region").append(item);
  window.setTimeout(() => item.remove(), 4200);
}

function storageForRemember(remember) {
  return remember ? localStorage : sessionStorage;
}

function loadSession() {
  localStorage.removeItem("kiln_admin_token");
  const storage = sessionStorage.getItem("kiln.admin.token") ? sessionStorage : localStorage;
  state.token = storage.getItem("kiln.admin.token") || "";
  const expiresAt = Date.parse(storage.getItem("kiln.admin.expires") || "");
  if (state.token && Number.isFinite(expiresAt) && expiresAt <= Date.now()) clearSession();
  $("remember-login").checked = Boolean(localStorage.getItem("kiln.admin.token"));
}

function saveSession(token, expiresAt, remember) {
  sessionStorage.removeItem("kiln.admin.token");
  sessionStorage.removeItem("kiln.admin.expires");
  localStorage.removeItem("kiln.admin.token");
  localStorage.removeItem("kiln.admin.expires");
  const storage = storageForRemember(remember);
  storage.setItem("kiln.admin.token", token);
  storage.setItem("kiln.admin.expires", expiresAt || "");
  state.token = token;
}

function clearSession() {
  state.token = "";
  state.me = null;
  sessionStorage.removeItem("kiln.admin.token");
  sessionStorage.removeItem("kiln.admin.expires");
  localStorage.removeItem("kiln.admin.token");
  localStorage.removeItem("kiln.admin.expires");
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (state.token) headers.set("Authorization", `Bearer ${state.token}`);
  const response = await fetch(path, { ...options, headers, cache: "no-store" });
  const requestId = response.headers.get("X-Request-ID") || "";
  const text = await response.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!response.ok) {
    if (response.status === 401 && state.token) logout(false);
    throw new ApiError(data?.error?.message || response.statusText || "请求失败", {
      code: data?.error?.code || "http_error",
      status: response.status,
      requestId,
    });
  }
  return data;
}

function showError(error, fallback = "操作失败") {
  const detail = [error?.message, error?.code, error?.requestId ? `请求 ${error.requestId}` : ""].filter(Boolean).join(" · ");
  toast(fallback, detail, "danger");
}

function setTheme(theme) {
  document.documentElement.dataset.theme = theme;
  localStorage.setItem("kiln.admin.theme", theme);
}

function initTheme() {
  const saved = localStorage.getItem("kiln.admin.theme");
  setTheme(saved || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"));
}

function pageHead(title, description, actions = []) {
  return h("div", { class: "page-head" },
    h("div", {}, h("h1", { text: title }), h("p", { text: description })),
    actions.length ? h("div", { class: "page-actions" }, actions) : null,
  );
}

function card(title, body, options = {}) {
  return h("section", { class: `card ${options.class || ""}`.trim() },
    title ? h("div", { class: "card-head" }, h("div", {}, h("h2", { text: title }), options.description ? h("p", { text: options.description }) : null), options.action || null) : null,
    h("div", { class: `card-body${options.flush ? " flush" : ""}` }, body),
  );
}

function emptyState(title, description, action) {
  return h("div", { class: "empty-state" }, h("div", {}, h("strong", { text: title }), h("p", { text: description }), action || null));
}

function routeInfo() {
  const path = location.pathname.replace(/\/+$/, "") || "/admin/overview";
  const segments = path.split("/").filter(Boolean);
  const section = segments[1] || "overview";
  return { path, section, id: segments[2] ? decodeURIComponent(segments[2]) : "" };
}

async function confirmDiscardIfDirty() {
  if (state.dirtyForm) {
    const leave = await confirmDialog("放弃未保存的更改？", "当前页面的修改尚未应用。", "放弃更改");
    if (!leave) return false;
  }
  state.dirtyForm = false;
  return true;
}

async function navigate(path) {
  if (!await confirmDiscardIfDirty()) return;
  history.pushState({}, "", path);
  renderRoute();
}

function setActiveNav(section) {
  document.querySelectorAll("[data-nav]").forEach((link) => {
    if (link.dataset.nav === section) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  });
  const names = { overview: "概览", channels: "频道", access: "访问", egress: "出站", settings: "设置" };
  $("page-title").textContent = names[section] || "控制台";
  document.title = `${names[section] || "控制台"} · Kiln`;
}

function stopPolling() {
  clearTimeout(state.pollTimer);
  state.pollTimer = null;
}

async function renderRoute() {
  if (!state.token) return showLogin();
  stopPolling();
  state.routeAbort?.abort();
  state.routeAbort = new AbortController();
  const route = routeInfo();
  setActiveNav(route.section);
  closeMobileNav();
  main.replaceChildren(h("div", { class: "skeleton skeleton-page" }));
  try {
    if (route.section === "channels" && route.id) await renderChannelDetail(route.id);
    else if (route.section === "channels") await renderChannels();
    else if (route.section === "access") await renderAccess();
    else if (route.section === "egress") await renderEgress();
    else if (route.section === "settings") await renderSettings();
    else await renderOverview();
    main.focus({ preventScroll: true });
  } catch (error) {
    if (error?.name === "AbortError") return;
    main.replaceChildren(pageHead("无法加载页面", "请求失败，但你的会话和其他数据仍然保留。"), emptyState("加载失败", error.message, button("重试", { kind: "primary", onClick: renderRoute })));
    showError(error, "页面加载失败");
  }
}

async function ensureCatalog() {
  const [channels, upstreams] = await Promise.all([api("/v1/admin/channels"), api("/v1/admin/upstreams")]);
  state.channels = [...(channels.channels || [])].sort((a, b) => (a.sort_order || 0) - (b.sort_order || 0));
  state.upstreams = upstreams.upstreams || [];
  state.searchIndex = null;
}

function metric(label, value, meta) {
  return h("article", { class: "metric-card" }, h("span", { text: label }), h("strong", { class: "metric-value", text: value }), h("span", { class: "metric-meta", text: meta }));
}

async function renderOverview() {
  const status = await api("/v1/status");
  const sessions = status.sessions || [];
  const sessionRows = sessions.map((session) => h("tr", {},
    h("td", { class: "mono", text: session.channel_id }),
    h("td", {}, statusBadge(session.state)),
    h("td", { text: session.mode?.toUpperCase() || "—" }),
    h("td", { class: "mono muted", text: session.pack_mode || "—" }),
    h("td", { class: "muted", text: session.last_error || "—" }),
    h("td", {}, h("div", { class: "row-actions" }, button("查看", { small: true, onClick: () => navigate(`/admin/channels/${encodeURIComponent(session.channel_id)}`) }), button("停止", { small: true, kind: "danger", onClick: () => stopSession(session.channel_id, renderOverview) }))),
  ));
  const table = sessions.length ? h("div", { class: "table-wrap" }, h("table", {},
    h("thead", {}, h("tr", {}, ...["频道", "状态", "类型", "打包模式", "最近错误", ""].map((label) => h("th", { text: label })))),
    h("tbody", {}, sessionRows),
  )) : emptyState("当前没有活动会话", "播放频道或执行预热后，会话会出现在这里。", button("查看频道", { onClick: () => navigate("/admin/channels") }));
  const healthBody = h("div", { class: "action-list" },
    h("div", { class: "action-item" }, h("span", {}, h("strong", { text: "HTTP 服务" }), h("small", { text: "健康检查通过" })), badge("正常", "success")),
    h("div", { class: "action-item" }, h("span", {}, h("strong", { text: "并发协程" }), h("small", { text: "Go runtime" })), h("strong", { text: formatNumber(status.goroutines) })),
    h("div", { class: "action-item" }, h("span", {}, h("strong", { text: "最后更新" }), h("small", { text: new Date().toLocaleTimeString("zh-Hans") })), badge("实时", "info")),
  );
  main.replaceChildren(
    pageHead("概览", "实例健康、流量与会话状态的一站式视图。", [button("刷新", { onClick: renderOverview })]),
    h("div", { class: "stack" },
      h("div", { class: "metrics" },
        metric("运行时间", formatDuration(status.uptime_sec), "当前进程"),
        metric("请求", formatNumber(status.requests), "累计 HTTP 请求"),
        metric("错误", formatNumber(status.errors), status.errors ? "需要关注" : "没有累计错误"),
        metric("入口流量", formatBytes(status.bytes_in), "从上游读取"),
        metric("出口流量", formatBytes(status.bytes_out), "发送给播放器"),
      ),
      h("div", { class: "grid-2" }, card("活动会话", table, { flush: true, description: `${sessions.length} 个会话` }), card("实例健康", healthBody)),
    ),
  );
  updateInstance(true, `${sessions.length} 个活动会话`);
  if (!document.hidden && routeInfo().section === "overview") state.pollTimer = window.setTimeout(renderOverview, 5000);
}

function channelLogo(channel) {
  const box = h("span", { class: "channel-logo", text: (channel.title || channel.id || "?").slice(0, 1).toUpperCase() });
  if (channel.logo_url) {
    const image = h("img", { src: channel.logo_url, alt: "", loading: "lazy", width: 38, height: 38 });
    image.addEventListener("load", () => box.replaceChildren(image), { once: true });
  }
  return box;
}

function channelCell(channel) {
  return h("div", { class: "channel-cell" }, channelLogo(channel), h("span", { class: "channel-copy" }, h("strong", { text: channel.title || channel.id }), h("small", { class: "mono", text: channel.id })));
}

async function renderChannels() {
  await ensureCatalog();
  let filtered = state.channels;
  const search = h("input", { type: "search", placeholder: "搜索名称、ID、拼音或粤拼…", autocomplete: "off", "aria-label": "搜索频道" });
  const tbody = h("tbody");
  const mobile = h("div", { class: "mobile-records" });
  const table = h("div", { class: "table-wrap desktop-table" }, h("table", {},
    h("thead", {}, h("tr", {}, ...["频道", "分组", "类型", "启动", "状态", ""].map((label) => h("th", { text: label })))), tbody,
  ));
  const listCard = card("频道目录", h("div", {}, table, mobile), { flush: true, description: `${state.channels.length} 个频道` });

  const draw = (channels) => {
    tbody.replaceChildren();
    mobile.replaceChildren();
    for (const channel of channels) {
      const open = () => navigate(`/admin/channels/${encodeURIComponent(channel.id)}`);
      const position = state.channels.findIndex((item) => item.id === channel.id);
      const reorderDisabled = Boolean(search.value.trim());
      tbody.append(h("tr", {},
        h("td", {}, channelCell(channel)), h("td", { text: channel.group || "未分组" }), h("td", {}, badge(channel.ingress?.toUpperCase() || "—")),
        h("td", { text: channel.on_demand ? "按需" : "常驻" }), h("td", {}, channel.disabled ? badge("已停用", "danger") : badge("已启用", "success")),
        h("td", {}, h("div", { class: "row-actions" }, button("↑", { small: true, disabled: reorderDisabled || position <= 0, ariaLabel: `上移 ${channel.title}`, onClick: () => moveChannel(channel.id, -1) }), button("↓", { small: true, disabled: reorderDisabled || position >= state.channels.length - 1, ariaLabel: `下移 ${channel.title}`, onClick: () => moveChannel(channel.id, 1) }), button("详情", { small: true, onClick: open }))),
      ));
      mobile.append(h("article", { class: "record-card" },
        h("div", { class: "record-card-head" }, channelCell(channel), channel.disabled ? badge("已停用", "danger") : badge("已启用", "success")),
        h("div", { class: "record-card-meta" }, h("span", {}, h("small", { text: "分组" }), channel.group || "未分组"), h("span", {}, h("small", { text: "入口" }), channel.ingress?.toUpperCase() || "—")),
        h("div", { class: "record-card-actions" }, button("上移", { disabled: reorderDisabled || position <= 0, onClick: () => moveChannel(channel.id, -1) }), button("下移", { disabled: reorderDisabled || position >= state.channels.length - 1, onClick: () => moveChannel(channel.id, 1) }), button("管理", { onClick: open })),
      ));
    }
    if (!channels.length) {
      const clear = () => { search.value = ""; draw(state.channels); };
      const empty = emptyState("没有匹配的频道", "尝试中文、拼音、粤拼、ID 或分组。", button("清除搜索", { onClick: clear }));
      tbody.append(h("tr", {}, h("td", { colSpan: 6 }, empty)));
      mobile.append(emptyState("没有匹配的频道", "尝试中文、拼音、粤拼、ID 或分组。", button("清除搜索", { onClick: clear })));
    }
  };
  draw(filtered);
  let searchTimer;
  search.addEventListener("input", () => {
    clearTimeout(searchTimer);
    searchTimer = window.setTimeout(async () => {
      try {
        if (!state.searchIndex) state.searchIndex = await buildSearchIndex(state.channels);
        filtered = await searchIndex(state.searchIndex, search.value, 1000);
        draw(filtered);
      } catch (error) { showError(error, "搜索索引加载失败"); }
    }, 90);
  });
  main.replaceChildren(
    pageHead("频道", "管理上游入口、启动策略与播放状态。", [button("导入 M3U", { onClick: showImportModal }), button("新建频道", { kind: "primary", onClick: () => navigate("/admin/channels/new") })]),
    h("div", { class: "toolbar" }, h("div", { class: "search-field" }, icon("⌕"), search), h("span", { class: "muted", text: `${state.channels.length} 个频道` })),
    listCard,
  );
}

async function moveChannel(id, delta) {
  const ids = state.channels.map((channel) => channel.id);
  const index = ids.indexOf(id);
  const target = index + delta;
  if (index < 0 || target < 0 || target >= ids.length) return;
  [ids[index], ids[target]] = [ids[target], ids[index]];
  try {
    const revisions = Object.fromEntries(state.channels.map((channel) => [channel.id, channel.revision]));
    await api("/v1/admin/channels/reorder", { method: "PUT", body: JSON.stringify({ ids, revisions }) });
    toast("频道顺序已更新");
    await renderChannels();
  } catch (error) { showError(error, "排序失败"); }
}

function inputFor(name, value = "", type = "text") {
  return h("input", { name, value: value ?? "", type });
}

async function renderChannelDetail(id) {
  state.dirtyForm = false;
  const isNew = id === "new";
  if (!state.upstreams.length) await ensureCatalog();
  let channel = {
    id: "", title: "", group: "", logo_url: "", upstream: state.upstreams.length === 1 ? state.upstreams[0].id : "",
    path: "", ingress: "hls", disabled: false, on_demand: true, autostart: false, idle_timeout_sec: 90,
    keys_file: "", user_agent: "", headers: {}, restart_on_failure: false, prefer_height: 0,
  };
  let revision = 0;
  if (!isNew) {
    const detail = await api(`/v1/admin/channels/${encodeURIComponent(id)}`);
    channel = detail.channel;
    revision = detail.revision || 0;
  }
  const form = h("form", { novalidate: true });
  const idInput = inputFor("id", channel.id);
  idInput.required = true;
  idInput.disabled = !isNew;
  const titleInput = inputFor("title", channel.title);
  titleInput.required = true;
  const groupInput = inputFor("group", channel.group);
  const logoInput = inputFor("logo_url", channel.logo_url, "url");
  const upstreamSelect = h("select", { name: "upstream", required: true }, h("option", { value: "", text: "选择上游" }), state.upstreams.map((upstream) => h("option", { value: upstream.id, selected: upstream.id === channel.upstream, text: `${upstream.id} · ${upstream.base_url}` })));
  const pathInput = inputFor("path", channel.path);
  pathInput.required = true;
  const ingressSelect = h("select", { name: "ingress" }, h("option", { value: "hls", selected: channel.ingress === "hls", text: "HLS" }), h("option", { value: "dash", selected: channel.ingress === "dash", text: "DASH" }));
  const strategySelect = h("select", { name: "strategy" },
    h("option", { value: "ondemand", selected: channel.on_demand && !channel.autostart, text: "按需启动" }),
    h("option", { value: "persistent", selected: !channel.on_demand && channel.autostart, text: "常驻运行" }),
    h("option", { value: "prewarm", selected: channel.on_demand && channel.autostart, text: "开机预热后允许回收" }),
  );
  const idleInput = inputFor("idle_timeout_sec", channel.idle_timeout_sec || 90, "number"); idleInput.min = "10";
  const keysInput = inputFor("keys_file", channel.keys_file);
  const heightInput = inputFor("prefer_height", channel.prefer_height || 0, "number"); heightInput.min = "0";
  const userAgentInput = inputFor("user_agent", channel.user_agent || ""); userAgentInput.placeholder = `跟随 Kiln ${state.version || "当前"} 版本`;
  const restartSelect = h("select", { name: "restart_on_failure" }, h("option", { value: "false", selected: !channel.restart_on_failure, text: "关闭（HLS 默认）" }), h("option", { value: "true", selected: channel.restart_on_failure, text: "开启" }));
  const syncIngressFields = () => { restartSelect.disabled = ingressSelect.value === "dash"; if (restartSelect.disabled) restartSelect.value = "true"; };
  ingressSelect.addEventListener("change", syncIngressFields);
  syncIngressFields();
  const headersInput = h("textarea", { name: "headers", value: Object.entries(channel.headers || {}).map(([key, value]) => `${key}: ${value}`).join("\n"), placeholder: "Header-Name: value\nAuthorization: 留空表示保留现有值" });
  const basic = h("section", { class: "form-section" }, h("div", { class: "form-section-head" }, h("h2", { text: "基本信息" }), h("p", { text: "频道 ID 创建后不可更改；显示名称可以随时调整。" })),
    h("div", { class: "form-grid" }, field("频道 ID", idInput, isNew ? "用于播放地址，只能包含安全的 URL 字符。" : "若需更换 ID，请复制为新频道。"), field("显示名称", titleInput), field("分组", groupInput), field("Logo 地址", logoInput), field("上游", upstreamSelect), field("播放路径", pathInput), field("入口类型", ingressSelect), field("启动策略", strategySelect)),
  );
  const advanced = h("section", { class: "form-section" }, h("div", { class: "form-section-head" }, h("h2", { text: "高级设置" }), h("p", { text: "留空的 User-Agent 会自动跟随 Kiln 构建版本。" })),
    h("div", { class: "form-grid" }, field("空闲回收（秒）", idleInput), field("首选高度", heightInput, "0 表示继承全局设置。"), field("DASH keys 文件", keysInput, "只显示路径，不读取文件内容。"), field("失败后自动重启", restartSelect, "DASH 频道始终开启。"), field("自定义 User-Agent", userAgentInput), h("div", { class: "field span-2" }, h("label", { text: "请求头" }), headersInput, h("p", { class: "field-hint", text: "每行一个。敏感值保存后不应再次回显。" }))),
  );
  const dirty = h("span", { class: "muted", text: "没有未保存更改" });
  const save = button(isNew ? "创建频道" : "保存更改", { kind: "primary", type: "submit" });
  form.append(basic, advanced, h("div", { class: "form-footer" }, dirty, h("div", { class: "form-footer-actions" }, button("取消", { onClick: () => navigate("/admin/channels") }), save)));
  form.addEventListener("input", () => { state.dirtyForm = true; dirty.textContent = "有未保存更改"; });
  form.addEventListener("change", () => { state.dirtyForm = true; dirty.textContent = "有未保存更改"; });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const data = new FormData(form);
    const strategy = data.get("strategy");
    const headers = {};
    String(data.get("headers") || "").split("\n").forEach((line) => {
      const split = line.indexOf(":");
      if (split > 0) headers[line.slice(0, split).trim()] = line.slice(split + 1).trim();
    });
    const body = {
      id: String(data.get("id") || channel.id).trim(), title: String(data.get("title") || "").trim(), group: String(data.get("group") || "").trim(), logo_url: String(data.get("logo_url") || "").trim(),
      upstream: String(data.get("upstream") || ""), path: String(data.get("path") || "").trim(), ingress: String(data.get("ingress") || "hls"), disabled: Boolean(channel.disabled),
      on_demand: strategy !== "persistent", autostart: strategy !== "ondemand", idle_timeout_sec: Number(data.get("idle_timeout_sec") || 90), keys_file: String(data.get("keys_file") || "").trim(),
      prefer_height: Number(data.get("prefer_height") || 0), user_agent: String(data.get("user_agent") || "").trim(), headers,
      restart_on_failure: String(data.get("ingress") || "hls") === "dash" || data.get("restart_on_failure") === "true",
    };
    if (!body.id || !body.title || !body.upstream || !body.path) return toast("请补全必填字段", "频道 ID、名称、上游和路径均为必填。", "danger");
    if (body.ingress === "dash" && !body.keys_file) return toast("DASH 频道需要 keys 文件", "请填写本地密钥文件路径。", "danger");
    save.disabled = true;
    try {
      const path = isNew ? "/v1/admin/channels" : `/v1/admin/channels/${encodeURIComponent(channel.id)}`;
      await api(path, { method: isNew ? "POST" : "PUT", headers: revision ? { "If-Match": String(revision) } : {}, body: JSON.stringify(body) });
      state.dirtyForm = false;
      toast(isNew ? "频道已创建" : "频道已保存");
      navigate(`/admin/channels/${encodeURIComponent(body.id)}`);
    } catch (error) { showError(error, "保存失败"); save.disabled = false; }
  });
  const actions = isNew ? null : h("div", { class: "stack" },
    card("运行操作", h("div", { class: "action-list" },
      actionItem("探测上游", "不启动播放会话", "探测", () => probeChannel(channel.id)),
      actionItem("预热频道", "提前完成冷启动", "预热", () => warmupChannel(channel.id)),
      actionItem("播放预览", "使用 5 分钟短期凭证", "预览", () => previewChannel(channel)),
      actionItem("停止会话", "下次播放将重新冷启动", "停止", () => stopSession(channel.id), "danger"),
    )),
    card("频道状态", h("div", { class: "action-list" }, actionItem(channel.disabled ? "重新启用" : "停用频道", channel.disabled ? "重新加入公开目录" : "隐藏并停止现有会话", channel.disabled ? "启用" : "停用", () => toggleChannel(channel), channel.disabled ? "secondary" : "danger"))),
    card("危险区域", h("div", { class: "action-list" }, actionItem("永久删除", "此操作无法撤销", "删除", () => confirmDeleteChannel(channel), "danger")), { class: "danger-zone" }),
  );
  main.replaceChildren(pageHead(isNew ? "新建频道" : channel.title || channel.id, isNew ? "从一个可验证的最小配置开始。" : `${channel.id} · ${channel.ingress?.toUpperCase()}`, [button("返回列表", { onClick: () => navigate("/admin/channels") })]), h("div", { class: "detail-layout" }, h("div", { class: "card" }, form), actions ? h("aside", { class: "stack" }, actions) : null));
}

function actionItem(title, description, label, onClick, kind = "secondary") {
  return h("div", { class: "action-item" }, h("span", {}, h("strong", { text: title }), h("small", { text: description })), button(label, { kind, small: true, onClick }));
}

async function probeChannel(id) {
  toast("正在探测", "正在连接上游…");
  try {
    const result = await api(`/v1/admin/channels/${encodeURIComponent(id)}/probe`, { method: "POST", body: "{}" });
    if (result.ok) toast("探测成功", `${result.status} · ${result.dur_ms} ms`);
    else toast("探测失败", result.error || "上游没有返回有效响应", "danger");
  } catch (error) { showError(error, "探测失败"); }
}

async function warmupChannel(id) {
  try { await api(`/v1/admin/channels/${encodeURIComponent(id)}/warmup`, { method: "POST", body: "{}" }); toast("预热已开始", "可在概览中查看启动状态。"); }
  catch (error) { showError(error, "无法预热频道"); }
}

async function stopSession(id, after) {
  const accepted = await confirmDialog("停止会话？", `频道 ${id} 的打包进程将停止，下次播放会重新冷启动。`, "停止会话");
  if (!accepted) return;
  try { await api(`/v1/admin/sessions/${encodeURIComponent(id)}`, { method: "DELETE" }); toast("会话已停止"); await after?.(); }
  catch (error) { showError(error, "停止失败"); }
}

async function toggleChannel(channel) {
  if (!await confirmDiscardIfDirty()) return;
  try {
    const detail = await api(`/v1/admin/channels/${encodeURIComponent(channel.id)}`);
    const full = detail.channel;
    full.disabled = !full.disabled;
    await api(`/v1/admin/channels/${encodeURIComponent(channel.id)}`, { method: "PUT", headers: detail.revision ? { "If-Match": String(detail.revision) } : {}, body: JSON.stringify(full) });
    toast(full.disabled ? "频道已停用" : "频道已启用");
    renderRoute();
  } catch (error) { showError(error, "状态更新失败"); renderRoute(); }
}

async function confirmDeleteChannel(channel) {
  const accepted = await confirmDialog("永久删除频道？", `输入频道 ID “${channel.id}” 后确认。该操作会停止现有会话。`, "永久删除", channel.id);
  if (!accepted) return;
  try {
    const detail = await api(`/v1/admin/channels/${encodeURIComponent(channel.id)}`);
    await api(`/v1/admin/channels/${encodeURIComponent(channel.id)}`, { method: "DELETE", headers: detail.revision ? { "If-Match": String(detail.revision) } : {} });
    state.dirtyForm = false;
    toast("频道已删除"); navigate("/admin/channels");
  }
  catch (error) { showError(error, "删除失败"); }
}

async function previewChannel(channel) {
  try {
    const preview = await api(`/v1/admin/channels/${encodeURIComponent(channel.id)}/preview`, { method: "POST", body: "{}" });
    const video = h("video", { class: "player-video", controls: true, autoplay: true, playsInline: true });
    showModal("播放预览", `${channel.title} · 凭证将在 ${new Date(preview.expires_at).toLocaleTimeString("zh-Hans")} 过期`, video, [button("关闭", { onClick: closeModal })]);
    if (video.canPlayType("application/vnd.apple.mpegurl")) video.src = preview.play_url;
    else {
      await loadScript("/admin/assets/third_party/hls.light.min.js");
      if (!window.Hls?.isSupported()) throw new Error("当前浏览器不支持 HLS Media Source");
      const player = new window.Hls({ enableWorker: true, lowLatencyMode: true });
      player.loadSource(preview.play_url);
      player.attachMedia(video);
      modal.addEventListener("close", () => player.destroy(), { once: true });
    }
  } catch (error) { showError(error, "无法打开预览"); }
}

function loadScript(src) {
  const existing = document.querySelector(`script[src="${src}"]`);
  if (existing) return Promise.resolve();
  return new Promise((resolve, reject) => document.head.append(h("script", { src, onLoad: resolve, onError: reject })));
}

function showImportModal() {
  const upstream = h("select", {}, h("option", { value: "", text: "选择默认上游" }), state.upstreams.map((item) => h("option", { value: item.id, text: item.id })));
  const content = h("textarea", { placeholder: "#EXTM3U\n#EXTINF:-1,频道名称\nhttps://…" });
  const result = h("div");
  const previewButton = button("生成预览", { kind: "primary", onClick: async () => {
    try {
      const data = await api("/v1/admin/import/m3u", { method: "POST", body: JSON.stringify({ content: content.value, default_upstream: upstream.value, apply: false }) });
      const existing = new Map(state.channels.map((channel) => [channel.id, channel]));
      const groups = { create: [], update: [], same: [], invalid: [] };
      for (const entry of data.entries || []) {
        if (entry.skip || !entry.suggested_id) { groups.invalid.push(entry); continue; }
        const current = existing.get(entry.suggested_id);
        if (!current) { groups.create.push(entry); continue; }
        const unchanged = current.title === entry.title && current.group === entry.group && current.path === entry.suggested_path && current.ingress === entry.suggested_ingress;
        groups[unchanged ? "same" : "update"].push(entry);
      }
      const summary = h("div", { class: "filter-row" }, badge(`新增 ${groups.create.length}`, "success"), badge(`更新 ${groups.update.length}`, "warning"), badge(`未变化 ${groups.same.length}`), badge(`跳过 ${groups.invalid.length}`, groups.invalid.length ? "danger" : ""));
      const changed = [...groups.create, ...groups.update, ...groups.invalid].slice(0, 50);
      const details = h("div", { class: "action-list" }, changed.map((entry) => h("div", { class: "action-item" }, h("span", {}, h("strong", { text: entry.title || entry.suggested_id || "无法识别" }), h("small", { text: entry.note || entry.suggested_path || "—" })), badge(entry.skip ? "跳过" : existing.has(entry.suggested_id) ? "更新" : "新增", entry.skip ? "danger" : existing.has(entry.suggested_id) ? "warning" : "success"))));
      result.replaceChildren(h("div", { class: "stack" }, h("div", { class: "inline-alert" }, h("span", { text: `已解析 ${data.count} 项。导入不会删除列表中缺失的频道。` })), summary, details));
      previewButton.dataset.entries = JSON.stringify(data.entries || []);
      applyButton.disabled = !(data.entries || []).length;
    } catch (error) { showError(error, "预览失败"); }
  }});
  const applyButton = button("确认导入", { kind: "primary", disabled: true, onClick: async () => {
    try {
      const entries = JSON.parse(previewButton.dataset.entries || "[]");
      const revisions = Object.fromEntries(state.channels.map((channel) => [channel.id, channel.revision]));
      const data = await api("/v1/admin/import/m3u", { method: "POST", body: JSON.stringify({ entries, default_upstream: upstream.value, revisions, apply: true }) });
      toast("导入完成", `写入 ${data.created} 项，跳过 ${data.skipped} 项`); closeModal(); renderChannels();
    } catch (error) { showError(error, "导入失败"); }
  }});
  showModal("批量导入 M3U", "高级工具 · 请先预览映射结果", h("div", { class: "stack" }, field("默认上游", upstream), field("M3U 内容", content), result), [button("取消", { onClick: closeModal }), previewButton, applyButton]);
}

async function renderAccess() {
  const [tokensData, logsData] = await Promise.all([api("/v1/admin/access-tokens"), api("/v1/admin/access-logs?limit=80")]);
  const tokens = tokensData.access_tokens || [];
  const logs = logsData.access_logs || [];
  const params = new URLSearchParams(location.search);
  const tab = params.get("tab") === "logs" ? "logs" : "tokens";
  const tabs = h("div", { class: "tabs", role: "tablist" },
    h("button", { role: "tab", "aria-selected": tab === "tokens", text: "访问链接", onClick: () => { history.replaceState({}, "", "/admin/access"); renderAccess(); } }),
    h("button", { role: "tab", "aria-selected": tab === "logs", text: "访问日志", onClick: () => { history.replaceState({}, "", "/admin/access?tab=logs"); renderAccess(); } }),
  );
  if (tab === "logs") {
    const rows = logs.map((log) => h("tr", {}, h("td", { class: "muted", text: formatTime(log.created_at) }), h("td", { class: "mono", text: log.token_prefix || "—" }), h("td", { class: "mono", text: log.path }), h("td", { text: log.channel_id || "—" }), h("td", {}, badge(String(log.status), log.status >= 400 ? "danger" : "success")), h("td", { class: "mono muted", text: log.remote || "—" })));
    const mobileLogs = h("div", { class: "mobile-records" }, logs.map((log) => h("article", { class: "record-card" }, h("div", { class: "record-card-head" }, h("strong", { text: log.channel_id || "播放列表" }), badge(String(log.status), log.status >= 400 ? "danger" : "success")), h("div", { class: "mono", text: log.path }), h("div", { class: "record-card-meta" }, h("span", {}, h("small", { text: "时间" }), formatTime(log.created_at)), h("span", {}, h("small", { text: "来源 IP" }), log.remote || "—")))));
    const table = logs.length ? h("div", {}, h("div", { class: "table-wrap desktop-table" }, h("table", {}, h("thead", {}, h("tr", {}, ...["时间", "Token", "路径", "频道", "状态", "IP"].map((label) => h("th", { text: label })))), h("tbody", {}, rows))), mobileLogs) : emptyState("暂无访问记录", "播放列表和播放入口请求会显示在这里。");
    main.replaceChildren(pageHead("访问", "分发链接与访问审计。", [tabs, button("清空日志", { kind: "danger", onClick: clearAccessLogs })]), card("访问日志", table, { flush: true, description: "最多保留 5000 条，并按保留期限清理" }));
    return;
  }
  const rows = tokens.map((token) => {
    const active = token.enabled && !token.revoked_at && (!token.expires_at || token.expires_at > Date.now() / 1000);
    return h("tr", {}, h("td", {}, h("strong", { text: token.name }), token.note ? h("div", { class: "muted", text: token.note }) : null), h("td", { class: "mono", text: `${token.token_prefix}…` }), h("td", { class: "mono", text: scopeLabel(token.scope) }), h("td", { text: token.expires_at ? formatTime(token.expires_at) : "永不过期" }), h("td", {}, active ? badge("有效", "success") : badge(token.revoked_at ? "已吊销" : "已过期", "danger")), h("td", {}, h("div", { class: "row-actions" }, active ? button("吊销", { small: true, kind: "danger", onClick: () => revokeToken(token) }) : null, button("删除", { small: true, kind: "ghost", onClick: () => deleteToken(token) }))));
  });
  const mobileTokens = h("div", { class: "mobile-records" }, tokens.map((token) => {
    const active = token.enabled && !token.revoked_at && (!token.expires_at || token.expires_at > Date.now() / 1000);
    return h("article", { class: "record-card" }, h("div", { class: "record-card-head" }, h("strong", { text: token.name }), active ? badge("有效", "success") : badge(token.revoked_at ? "已吊销" : "已过期", "danger")), h("div", { class: "record-card-meta" }, h("span", {}, h("small", { text: "Token" }), h("span", { class: "mono", text: `${token.token_prefix}…` })), h("span", {}, h("small", { text: "到期" }), token.expires_at ? formatTime(token.expires_at) : "永不过期")), h("div", { class: "record-card-actions" }, active ? button("吊销", { kind: "danger", onClick: () => revokeToken(token) }) : null, button("删除记录", { onClick: () => deleteToken(token) })));
  }));
  const table = tokens.length ? h("div", {}, h("div", { class: "table-wrap desktop-table" }, h("table", {}, h("thead", {}, h("tr", {}, ...["名称", "前缀", "范围", "到期", "状态", ""].map((label) => h("th", { text: label })))), h("tbody", {}, rows))), mobileTokens) : emptyState("还没有访问链接", "创建一个受频道范围与有效期限制的播放列表链接。", button("创建链接", { kind: "primary", onClick: showCreateTokenModal }));
  main.replaceChildren(pageHead("访问", "创建可吊销、可审计的播放链接。", [tabs, button("创建访问链接", { kind: "primary", onClick: showCreateTokenModal })]), card("访问链接", table, { flush: true, description: `${tokens.length} 个记录` }));
}

function scopeLabel(raw) {
  try { const value = JSON.parse(raw); return value.all ? "全部频道" : (value.channel_ids || []).join(", ") || "无频道"; }
  catch { return raw || "全部频道"; }
}

async function showCreateTokenModal() {
  if (!state.channels.length) await ensureCatalog();
  const name = inputFor("name", ""); name.required = true;
  const note = inputFor("note", "");
  const expiry = h("select", {}, h("option", { value: "86400", text: "1 天" }), h("option", { value: "604800", text: "7 天" }), h("option", { value: "2592000", selected: true, text: "30 天" }), h("option", { value: "0", text: "永不过期" }));
  const all = h("input", { type: "checkbox", checked: true });
  const checks = state.channels.map((channel) => h("label", { class: "check-row" }, h("input", { type: "checkbox", value: channel.id, disabled: true }), h("span", { text: channel.title || channel.id })));
  all.addEventListener("change", () => checks.forEach((label) => { label.querySelector("input").disabled = all.checked; }));
  const body = h("div", { class: "stack" }, field("名称", name), field("备注", note), field("有效期", expiry), h("div", { class: "field" }, h("span", { class: "field-label", text: "频道范围" }), h("label", { class: "check-row" }, all, h("span", { text: "允许全部频道" })), h("div", { class: "action-list" }, checks)));
  const create = button("创建链接", { kind: "primary", onClick: async () => {
    const channelIDs = all.checked ? [] : checks.map((label) => label.querySelector("input")).filter((input) => input.checked).map((input) => input.value);
    if (!name.value.trim()) return toast("请填写名称", "名称用于以后识别这个链接。", "danger");
    try {
      const data = await api("/v1/admin/access-tokens", { method: "POST", body: JSON.stringify({ name: name.value.trim(), note: note.value.trim(), channel_ids: channelIDs, expires_in_sec: Number(expiry.value) }) });
      showOneTimeToken(data);
    } catch (error) { showError(error, "创建失败"); }
  }});
  showModal("创建访问链接", "明文 token 只会显示一次。", body, [button("取消", { onClick: closeModal }), create]);
}

function showOneTimeToken(data) {
  const token = h("div", { class: "secret-value mono", text: data.token });
  const url = h("div", { class: "secret-value mono", text: data.playlist_url });
  const body = h("div", { class: "stack" }, h("div", { class: "inline-alert warning", text: "请立即复制并安全保存。关闭后无法再次查看明文。" }), h("div", { class: "secret-box" }, h("strong", { text: "Token" }), token, button("复制 Token", { small: true, onClick: () => copyText(data.token, "Token 已复制") })), h("div", { class: "secret-box" }, h("strong", { text: "播放列表地址" }), url, button("复制地址", { small: true, onClick: () => copyText(data.playlist_url, "地址已复制") })));
  showModal("访问链接已创建", data.expires_at ? `有效至 ${formatTime(data.expires_at)}` : "永久有效", body, [button("复制地址并关闭", { kind: "primary", onClick: async () => { await copyText(data.playlist_url, "地址已复制"); closeModal(); renderAccess(); } })]);
}

async function revokeToken(token) {
  if (!await confirmDialog("吊销访问链接？", "所有使用该 token 的播放器会立即失去访问权限。", "吊销")) return;
  try { await api(`/v1/admin/access-tokens/${encodeURIComponent(token.id)}/revoke`, { method: "POST", headers: { "If-Match": String(token.revision || 0) }, body: "{}" }); toast("链接已吊销"); renderAccess(); }
  catch (error) { showError(error, "吊销失败"); }
}

async function deleteToken(token) {
  if (!await confirmDialog("删除 token 记录？", "删除不会比吊销更安全，但会减少审计信息。", "永久删除")) return;
  try { await api(`/v1/admin/access-tokens/${encodeURIComponent(token.id)}`, { method: "DELETE", headers: { "If-Match": String(token.revision || 0) } }); toast("记录已删除"); renderAccess(); }
  catch (error) { showError(error, "删除失败"); }
}

async function clearAccessLogs() {
  if (!await confirmDialog("清空访问日志？", "IP、路径和访问时间将永久删除。", "清空日志")) return;
  try { const data = await api("/v1/admin/access-logs", { method: "DELETE" }); toast("日志已清空", `删除 ${data.deleted || 0} 条`); renderAccess(); }
  catch (error) { showError(error, "清理失败"); }
}

async function renderEgress(draft = null) {
  if (!state.channels.length) await ensureCatalog();
  const data = draft || await api("/v1/admin/egress");
  if (!draft) state.dirtyForm = false;
  const markDirty = () => { state.dirtyForm = true; };
  const draftPayload = () => ({
    default: defaultProxy.value,
    playlist_policy: policy.value,
    docker_proxy_host: dockerHost.value.trim(),
    proxies: data.proxies || [],
    rules: data.rules || [],
  });
  const defaultProxy = h("select", {}, h("option", { value: "direct", text: "direct · 直接连接" }), (data.proxies || []).map((proxy) => h("option", { value: proxy.id, selected: proxy.id === data.default, text: `${proxy.id} · ${proxy.name || "代理"}` })));
  const policy = h("select", {}, ...["rewrite", "passthrough", "auto"].map((value) => h("option", { value, selected: value === data.playlist_policy, text: value })));
  const dockerHost = inputFor("docker_proxy_host", data.docker_proxy_host || "host.docker.internal");
  defaultProxy.addEventListener("change", () => { data.default = defaultProxy.value; markDirty(); });
  policy.addEventListener("change", () => { data.playlist_policy = policy.value; markDirty(); });
  dockerHost.addEventListener("input", () => { data.docker_proxy_host = dockerHost.value; markDirty(); });
  const refreshDraft = () => renderEgress(data);
  const proxyRows = (data.proxies || []).map((proxy) => h("tr", {}, h("td", {}, h("strong", { text: proxy.name || proxy.id }), h("div", { class: "mono muted", text: proxy.id })), h("td", { class: "mono", text: safeProxyURL(proxy.url) }), h("td", {}, proxyHasCredentials(proxy) ? badge("凭据已配置", "success") : badge("无凭据")), h("td", {}, proxy.disabled ? badge("停用", "danger") : badge("启用", "success")), h("td", {}, h("div", { class: "row-actions" }, button(proxy.disabled ? "启用" : "停用", { small: true, onClick: () => { proxy.disabled = !proxy.disabled; markDirty(); refreshDraft(); } }), button("移除", { small: true, kind: "danger", onClick: () => { data.proxies = data.proxies.filter((item) => item.id !== proxy.id); data.rules = (data.rules || []).filter((rule) => (rule.proxy || rule.proxy_id) !== proxy.id); if (data.default === proxy.id) data.default = "direct"; markDirty(); refreshDraft(); } })))));
  const ruleRows = (data.rules || []).map((rule) => h("tr", {}, h("td", { class: "mono", text: rule.id }), h("td", { text: String(rule.priority) }), h("td", { class: "mono", text: `${rule.kind} · ${rule.pattern}` }), h("td", { class: "mono", text: rule.proxy || rule.proxy_id || "direct" }), h("td", {}, rule.disabled ? badge("停用") : badge("启用", "success")), h("td", {}, h("div", { class: "row-actions" }, button(rule.disabled ? "启用" : "停用", { small: true, onClick: () => { rule.disabled = !rule.disabled; markDirty(); refreshDraft(); } }), button("移除", { small: true, kind: "danger", onClick: () => { data.rules = data.rules.filter((item) => item.id !== rule.id); markDirty(); refreshDraft(); } })))));
  const meta = h("div", { class: "form-grid" }, field("默认代理", defaultProxy), field("播放列表策略", policy), field("容器代理主机", dockerHost, "仅用于容器内 FFmpeg 访问宿主机代理。"));
  const testURL = inputFor("url", "", "url");
  const testChannel = h("select", {}, h("option", { value: "", text: "不指定频道" }), state.channels.map((channel) => h("option", { value: channel.id, text: channel.title || channel.id })));
  const testResult = h("div");
  const apply = button("应用更改", { kind: "primary", onClick: async () => {
    try {
      await api("/v1/admin/egress", { method: "PUT", headers: { "If-Match": String(data.revision || 0) }, body: JSON.stringify(draftPayload()) });
      state.dirtyForm = false;
      toast("出站策略已原子应用");
      await renderEgress();
    }
    catch (error) { showError(error, "应用失败"); }
  }});
  const test = button("测试路由", { onClick: async () => {
    try {
      const result = await api("/v1/admin/egress/test", { method: "POST", body: JSON.stringify({ url: testURL.value.trim(), channel_id: testChannel.value, draft: draftPayload() }) });
      testResult.replaceChildren(h("div", { class: `inline-alert ${result.ok ? "" : "danger"}`.trim(), text: result.ok ? `成功 · ${result.via_proxy} · ${result.dur_ms} ms · HTTP ${result.status}` : `失败 · ${result.error || "未知错误"}` }));
    } catch (error) { showError(error, "路由测试失败"); }
  }});
  const proxies = (data.proxies || []).length ? h("div", { class: "table-wrap" }, h("table", {}, h("thead", {}, h("tr", {}, ...["代理", "地址", "认证", "状态", "操作"].map((label) => h("th", { text: label })))), h("tbody", {}, proxyRows))) : emptyState("暂无代理", "当前所有流量将直接连接上游。");
  const rules = (data.rules || []).length ? h("div", { class: "table-wrap" }, h("table", {}, h("thead", {}, h("tr", {}, ...["规则", "优先级", "匹配", "出口", "状态", "操作"].map((label) => h("th", { text: label })))), h("tbody", {}, ruleRows))) : emptyState("暂无路由规则", "默认出口会应用于所有未匹配的请求。");
  main.replaceChildren(pageHead("出站", "所有修改先留在草稿中，测试通过后一次性应用。", [apply]), h("div", { class: "stack" }, card("全局策略", meta), card("代理配置", proxies, { flush: true, action: button("添加代理", { small: true, onClick: () => showProxyModal(data, refreshDraft) }) }), card("路由规则", rules, { flush: true, action: button("添加规则", { small: true, onClick: () => showRuleModal(data, refreshDraft) }) }), card("草稿路由测试", h("div", { class: "stack" }, h("div", { class: "form-grid" }, field("测试地址", testURL), field("频道上下文", testChannel)), h("div", {}, test), testResult), { description: "测试会使用尚未应用的代理和规则，不会改动线上配置。" })));
}

function showProxyModal(egress, after) {
  const id = inputFor("id"); const name = inputFor("name"); const url = inputFor("url", "", "url");
  showModal("添加代理", "凭据只写入，不会在应用后回显。", h("div", { class: "stack" }, field("代理 ID", id), field("显示名称", name), field("代理 URL", url, "支持 http、https、socks5 与 socks5h。")), [button("取消", { onClick: closeModal }), button("加入草稿", { kind: "primary", onClick: async () => {
    const proxyID = id.value.trim();
    if (!proxyID || !url.value.trim()) return toast("请填写代理 ID 和 URL", "", "danger");
    if ((egress.proxies || []).some((item) => item.id === proxyID)) return toast("代理 ID 已存在", "请先移除同名草稿项。", "danger");
    egress.proxies = [...(egress.proxies || []), { id: proxyID, name: name.value.trim(), url: url.value.trim(), disabled: false }];
    state.dirtyForm = true;
    closeModal(); after();
  } })]);
}

function showRuleModal(egress, after) {
  const id = inputFor("id"); const priority = inputFor("priority", "100", "number"); const pattern = inputFor("pattern");
  const kind = h("select", {}, ...["host_suffix", "host_exact", "host_regex", "channel_id", "url_regex"].map((value) => h("option", { value, text: value })));
  const proxy = h("select", {}, h("option", { value: "direct", text: "direct" }), (egress.proxies || []).map((item) => h("option", { value: item.id, text: item.id })));
  showModal("添加路由规则", "规则只会加入草稿；请先测试再应用。", h("div", { class: "form-grid" }, field("规则 ID", id), field("优先级", priority), field("匹配类型", kind), field("出口", proxy), h("div", { class: "span-2" }, field("匹配模式", pattern))), [button("取消", { onClick: closeModal }), button("加入草稿", { kind: "primary", onClick: () => {
    const ruleID = id.value.trim();
    if (!ruleID) return toast("请填写规则 ID", "", "danger");
    if ((egress.rules || []).some((item) => item.id === ruleID)) return toast("规则 ID 已存在", "请先移除同名草稿项。", "danger");
    egress.rules = [...(egress.rules || []), { id: ruleID, priority: Number(priority.value), kind: kind.value, pattern: pattern.value.trim(), proxy: proxy.value, disabled: false }];
    state.dirtyForm = true;
    closeModal(); after();
  } })]);
}

async function renderSettings() {
  state.dirtyForm = false;
  const data = await api("/v1/admin/settings");
  const base = inputFor("public_base_url", data.public_base_url || "", "url");
  const retention = inputFor("access_log_retention_days", data.access_log_retention_days || "30", "number");
  retention.min = "1"; retention.max = "3650";
  const save = button("保存设置", { kind: "primary", onClick: async () => {
    try {
      await api("/v1/admin/settings", { method: "PUT", headers: { "If-Match": String(data.revision || 0) }, body: JSON.stringify({ public_base_url: base.value.trim(), access_log_retention_days: retention.value }) });
      state.dirtyForm = false;
      toast("设置已保存");
      await renderSettings();
    }
    catch (error) { showError(error, "保存失败"); }
  }});
  [base, retention].forEach((control) => control.addEventListener("input", () => { state.dirtyForm = true; }));
  const readonly = h("div", { class: "action-list" },
    actionItemReadonly("监听地址", data.listen || "—"), actionItemReadonly("播放鉴权", data.play_require_auth ? "已启用" : "未启用"), actionItemReadonly("CORS 来源", (data.cors_origins || []).join(", ") || "同源"), actionItemReadonly("公开主机", (data.public_hosts || []).join(", ") || "未限制"),
  );
  main.replaceChildren(pageHead("设置", "只开放可安全热更新的实例设置。", [save]), h("div", { class: "grid-2" }, card("运行时设置", h("div", { class: "stack" }, field("Public Base URL", base, "用于生成播放链接；保存时会移除末尾斜杠。"), field("访问日志保留天数", retention, "同时最多保留 5000 条。"))), card("只读运行配置", readonly)));
}

function actionItemReadonly(label, value) {
  return h("div", { class: "action-item" }, h("span", {}, h("strong", { text: label })), h("span", { class: "mono muted", text: value }));
}

function showModal(title, description, body, actions = []) {
  $("modal-content").replaceChildren(h("div", { class: "modal-head" }, h("div", {}, h("h2", { text: title }), description ? h("p", { text: description }) : null), h("button", { class: "icon-button", type: "button", "aria-label": "关闭", text: "×", onClick: closeModal })), h("div", { class: "modal-body" }, body), actions.length ? h("div", { class: "modal-actions" }, actions) : null);
  modal.showModal();
}

function closeModal() {
  modal.close();
  $("modal-content").replaceChildren();
}

function confirmDialog(title, description, confirmLabel, expected = "") {
  return new Promise((resolve) => {
    const input = expected ? inputFor("confirmation") : null;
    if (input) input.placeholder = expected;
    const confirm = button(confirmLabel, { kind: "danger", disabled: Boolean(expected), onClick: () => { closeModal(); resolve(true); } });
    input?.addEventListener("input", () => { confirm.disabled = input.value !== expected; });
    showModal(title, description, h("div", { class: "stack" }, h("div", { class: "inline-alert danger", text: "这是不可撤销或会中断播放的操作。" }), input ? field("输入确认内容", input) : null), [button("取消", { onClick: () => { closeModal(); resolve(false); } }), confirm]);
    modal.addEventListener("cancel", (event) => { event.preventDefault(); closeModal(); resolve(false); }, { once: true });
  });
}

async function copyText(value, message) {
  await navigator.clipboard.writeText(value);
  toast(message);
}

function updateInstance(ok, detail) {
  $("instance-dot").className = `status-dot ${ok ? "ok" : "bad"}`;
  $("instance-state").textContent = ok ? "服务正常" : "服务异常";
  $("instance-detail").textContent = detail;
}

function openMobileNav() {
  app.classList.add("mobile-nav-open");
  $("mobile-scrim").hidden = false;
  $("sidebar").inert = false;
}

function closeMobileNav() {
  app.classList.remove("mobile-nav-open");
  $("mobile-scrim").hidden = true;
  $("sidebar").inert = matchMedia("(max-width: 900px)").matches;
}

function syncResponsiveNavigation() {
  if (matchMedia("(max-width: 900px)").matches) closeMobileNav();
  else $("sidebar").inert = false;
}

function showLogin() {
  stopPolling();
  $("login-view").classList.remove("is-hidden");
  app.classList.add("is-hidden");
  $("login-password").value = "";
  $("login-user").focus();
}

async function showApp() {
  $("login-view").classList.add("is-hidden");
  app.classList.remove("is-hidden");
  $("account-name").textContent = state.me.username;
  $("account-avatar").textContent = (state.me.username || "A").slice(0, 1).toUpperCase();
  await renderRoute();
}

function logout(notify = true) {
  clearSession();
  if (notify) toast("已退出登录");
  showLogin();
}

async function login(event) {
  event.preventDefault();
  const error = $("login-error");
  error.textContent = "";
  const submit = event.submitter;
  submit.disabled = true;
  try {
    const result = await api("/v1/auth/login", { method: "POST", body: JSON.stringify({ username: $("login-user").value.trim(), password: $("login-password").value }) });
    saveSession(result.token, result.expires_at, $("remember-login").checked);
    state.me = { username: result.username, role: result.role };
    await showApp();
  } catch (apiError) {
    error.textContent = apiError.message;
    $("login-password").select();
  } finally { submit.disabled = false; }
}

async function openCommand() {
  if (!state.channels.length) {
    try { await ensureCatalog(); } catch (error) { showError(error, "无法加载频道搜索"); }
  }
  $("command-dialog").showModal();
  $("command-input").value = "";
  $("command-input").focus();
  drawCommands("");
}

async function drawCommands(query) {
  const target = $("command-results");
  const routes = [
    { title: "概览", id: "page-overview", group: "页面", route: "/admin/overview" }, { title: "频道", id: "page-channels", group: "页面", route: "/admin/channels" },
    { title: "访问链接", id: "page-access", group: "页面", route: "/admin/access" }, { title: "出站代理", id: "page-egress", group: "页面", route: "/admin/egress" }, { title: "设置", id: "page-settings", group: "页面", route: "/admin/settings" },
  ];
  let channels = state.channels;
  if (query) {
    try { if (!state.searchIndex) state.searchIndex = await buildSearchIndex(state.channels); channels = await searchIndex(state.searchIndex, query, 20); }
    catch { channels = state.channels.filter((item) => `${item.title} ${item.id}`.toLowerCase().includes(query.toLowerCase())); }
  } else channels = channels.slice(0, 8);
  const matches = [...routes.filter((item) => !query || item.title.includes(query)), ...channels.map((channel) => ({ title: channel.title || channel.id, id: channel.id, group: `${channel.group || "未分组"} · ${channel.ingress?.toUpperCase()}`, route: `/admin/channels/${encodeURIComponent(channel.id)}` }))];
  target.replaceChildren(...matches.map((item, index) => h("button", { class: "command-option", role: "option", "aria-selected": index === 0, onClick: () => { $("command-dialog").close(); navigate(item.route); } }, channelLogo({ title: item.title }), h("span", { class: "command-option-copy" }, h("strong", { text: item.title }), h("small", { text: item.group })), badge(item.group === "页面" ? "页面" : "频道"))));
  if (!matches.length) target.append(emptyState("没有找到结果", "尝试更短的拼音、粤拼或频道 ID。"));
}

function moveCommandSelection(delta) {
  const options = [...$("command-results").querySelectorAll(".command-option")];
  if (!options.length) return;
  const current = options.findIndex((option) => option.getAttribute("aria-selected") === "true");
  const next = (Math.max(0, current) + delta + options.length) % options.length;
  options.forEach((option, index) => option.setAttribute("aria-selected", String(index === next)));
  options[next].scrollIntoView({ block: "nearest" });
}

function bindEvents() {
  document.addEventListener("click", (event) => {
    const link = event.target.closest("[data-route]");
    if (!link) return;
    event.preventDefault();
    navigate(link.getAttribute("href"));
  });
  window.addEventListener("popstate", () => {
    if (state.suppressNextPop) {
      state.suppressNextPop = false;
      return;
    }
    if (state.dirtyForm && !window.confirm("当前页面有未保存的更改，确定离开吗？")) {
      state.suppressNextPop = true;
      history.go(1);
      return;
    }
    state.dirtyForm = false;
    renderRoute();
  });
  window.addEventListener("beforeunload", (event) => {
    if (!state.dirtyForm) return;
    event.preventDefault();
    event.returnValue = "";
  });
  document.addEventListener("visibilitychange", () => { if (!document.hidden && state.token && !state.dirtyForm) renderRoute(); else stopPolling(); });
  $("login-form").addEventListener("submit", login);
  $("logout-button").addEventListener("click", () => logout());
  $("account-button").addEventListener("click", () => {
    const menu = $("account-menu"); menu.classList.toggle("is-hidden"); $("account-button").setAttribute("aria-expanded", String(!menu.classList.contains("is-hidden")));
  });
  $("theme-toggle").addEventListener("click", () => setTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark"));
  $("sidebar-collapse").addEventListener("click", () => { app.classList.toggle("sidebar-compact"); localStorage.setItem("kiln.admin.sidebar", app.classList.contains("sidebar-compact") ? "compact" : "full"); });
  $("mobile-menu").addEventListener("click", openMobileNav);
  $("mobile-scrim").addEventListener("click", closeMobileNav);
  matchMedia("(max-width: 900px)").addEventListener("change", syncResponsiveNavigation);
  $("command-trigger").addEventListener("click", openCommand);
  $("command-input").addEventListener("input", (event) => drawCommands(event.target.value));
  $("command-input").addEventListener("keydown", (event) => {
    if (event.isComposing) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      moveCommandSelection(event.key === "ArrowDown" ? 1 : -1);
    } else if (event.key === "Enter") {
      event.preventDefault();
      $("command-results").querySelector('.command-option[aria-selected="true"]')?.click();
    }
  });
  document.addEventListener("keydown", (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") { event.preventDefault(); openCommand(); }
  });
}

async function bootstrap() {
  initTheme();
  loadSession();
  bindEvents();
  syncResponsiveNavigation();
  if (localStorage.getItem("kiln.admin.sidebar") === "compact") app.classList.add("sidebar-compact");
  try {
    const root = await fetch("/", { headers: { Accept: "application/json" }, cache: "no-store" }).then((response) => response.json());
    state.version = root.version || "";
    $("login-version").textContent = state.version ? `Kiln ${state.version}` : "Kiln";
  } catch { /* Version is decorative. */ }
  if (!state.token) return showLogin();
  try { state.me = await api("/v1/me"); await showApp(); }
  catch { logout(false); }
}

bootstrap();
