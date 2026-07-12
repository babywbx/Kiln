import { formatTime, frag, h } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { loadCatalog, store } from "/admin/assets/core/store.js";
import { badge, button, card, emptyState, field, input, notice, pageHead, select, table } from "/admin/assets/ui/kit.js";
import { closeModal, confirmDialog, copyText, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";

const LOG_LIMIT = 100;

export async function renderAccess(ctx) {
  const tab = ctx.query.get("tab") === "logs" ? "logs" : "tokens";
  const tabs = h(
    "div",
    { class: "tabs", role: "tablist" },
    h("button", { type: "button", role: "tab", "aria-selected": String(tab === "tokens"), text: "访问密钥", onClick: () => ctx.navigate("/admin/access") }),
    h("button", { type: "button", role: "tab", "aria-selected": String(tab === "logs"), text: "访问日志", onClick: () => ctx.navigate("/admin/access?tab=logs") }),
  );

  if (tab === "logs") return frag(...(await renderLogs(ctx, tabs)));
  return frag(...(await renderTokens(ctx, tabs)));
}

async function renderTokens(ctx, tabs) {
  const data = await endpoints.tokens(ctx.signal);
  if (!ctx.alive()) return [];
  const tokens = data.access_tokens || [];

  const body = tokens.length
    ? table(
        ["名称", "密钥前缀", "频道范围", "到期时间", "状态", ""],
        tokens.map((token) => {
          const active = isActive(token);
          return h(
            "tr",
            {},
            h("td", {}, h("strong", { text: token.name }), token.note ? h("div", { class: "muted", text: token.note }) : null),
            h("td", { class: "mono", text: `${token.token_prefix}…` }),
            h("td", { class: "muted truncate", text: scopeLabel(token.scope) }),
            h("td", { class: "muted", text: token.expires_at ? formatTime(token.expires_at) : "永不过期" }),
            h("td", {}, active ? badge("有效", "success") : badge(token.revoked_at ? "已停用" : "已过期", "danger")),
            h(
              "td",
              {},
              h(
                "div",
                { class: "row-actions" },
                active ? button("停用", { kind: "danger", size: "small", onClick: () => revoke(ctx, token) }) : null,
                button("删除", { kind: "ghost", size: "small", onClick: () => remove(ctx, token) }),
              ),
            ),
          );
        }),
      )
    : emptyState("还没有访问密钥", "创建一个受频道范围与有效期限制的播放地址。", button("创建访问密钥", { kind: "primary", iconName: "plus", onClick: () => openCreateModal(ctx) }));

  return [
    pageHead("访问控制", "创建可停用、可审计的播放访问密钥。", [
      button("创建访问密钥", { kind: "primary", iconName: "plus", onClick: () => openCreateModal(ctx) }),
    ]),
    tabs,
    card({ title: "访问密钥", description: `${tokens.length} 个记录`, body, flush: true }),
  ];
}

async function renderLogs(ctx, tabs) {
  const data = await endpoints.accessLogs(LOG_LIMIT, ctx.signal);
  if (!ctx.alive()) return [];
  const logs = data.access_logs || [];

  const body = logs.length
    ? table(
        ["时间", "密钥前缀", "请求路径", "频道", "状态", "来源地址"],
        logs.map((log) =>
          h(
            "tr",
            {},
            h("td", { class: "muted", text: formatTime(log.created_at) }),
            h("td", { class: "mono", text: log.token_prefix || "—" }),
            h("td", { class: "mono truncate", text: log.path }),
            h("td", { text: log.channel_id || "—" }),
            h("td", {}, badge(String(log.status), log.status >= 400 ? "danger" : "success")),
            h("td", { class: "mono muted", text: log.remote || "—" }),
          ),
        ),
      )
    : emptyState("暂无访问记录", "播放列表和播放入口的请求会显示在这里。");

  const clear = async () => {
    const accepted = await confirmDialog({
      title: "清空访问日志？",
      description: "所有来源 IP、请求路径和访问时间将被永久删除。",
      warning: "清空后无法恢复，历史审计信息将全部丢失。",
      confirmLabel: "清空日志",
    });
    if (!accepted) return;
    try {
      const result = await endpoints.clearAccessLogs();
      toast("日志已清空", `删除 ${result.deleted || 0} 条`);
      await ctx.reload();
    } catch (error) {
      toastError(error, "清理失败");
    }
  };

  return [
    pageHead("访问控制", "查看播放列表与播放入口的访问记录。", [button("清空日志", { kind: "danger", iconName: "trash-2", onClick: clear })]),
    tabs,
    card({ title: "访问日志", description: `最近 ${logs.length} 条 · 最多保留 5000 条并按设置的期限自动清理`, body, flush: true }),
  ];
}

function isActive(token) {
  return token.enabled && !token.revoked_at && (!token.expires_at || token.expires_at > Date.now() / 1000);
}

// The server encodes scope as the literal "all", or a JSON array of channel ids.
function scopeLabel(raw) {
  const value = String(raw || "").trim();
  if (!value || value === "all") return "全部频道";
  try {
    const ids = JSON.parse(value);
    if (!Array.isArray(ids) || !ids.length) return "无频道";
    return ids.join("、");
  } catch {
    return value;
  }
}

async function openCreateModal(ctx) {
  await loadCatalog({});

  const nameInput = input("name", "", { required: true, placeholder: "客厅电视" });
  const noteInput = input("note", "", { placeholder: "可选备注" });
  const expiry = select("expires_in_sec", [
    ["86400", "1 天"],
    ["604800", "7 天"],
    ["2592000", "30 天"],
    ["0", "永不过期"],
  ], "2592000");

  const allToggle = h("input", { type: "checkbox", checked: true });
  const channelChecks = store.channels.map((channel) =>
    h(
      "label",
      { class: "check-row" },
      h("input", { type: "checkbox", value: channel.id, disabled: true }),
      h("span", { text: channel.title || channel.id }),
    ),
  );
  allToggle.addEventListener("change", () => {
    for (const label of channelChecks) label.querySelector("input").disabled = allToggle.checked;
  });

  const create = button("创建访问密钥", {
    kind: "primary",
    onClick: async () => {
      if (!nameInput.value.trim()) {
        nameInput.setAttribute("aria-invalid", "true");
        toast("请填写名称", "名称用于以后识别这个密钥。", "danger");
        return;
      }
      const channelIDs = allToggle.checked
        ? []
        : channelChecks.map((label) => label.querySelector("input")).filter((box) => box.checked).map((box) => box.value);
      try {
        const data = await endpoints.createToken({
          name: nameInput.value.trim(),
          note: noteInput.value.trim(),
          channel_ids: channelIDs,
          expires_in_sec: Number(expiry.value),
        });
        showSecret(ctx, data);
      } catch (error) {
        toastError(error, "创建失败");
      }
    },
  });

  openModal({
    title: "创建访问密钥",
    description: "完整密钥只会显示一次。",
    body: h(
      "div",
      { class: "stack" },
      field("名称", nameInput),
      field("备注", noteInput),
      field("有效期", expiry),
      h(
        "div",
        { class: "field" },
        h("span", { class: "field-label", text: "频道范围" }),
        h("label", { class: "check-row" }, allToggle, h("span", { text: "允许全部频道" })),
        h("div", { class: "check-list" }, channelChecks),
      ),
    ),
    actions: [button("取消", { onClick: closeModal }), create],
  });
}

function showSecret(ctx, data) {
  openModal({
    title: "访问密钥已创建",
    description: data.expires_at ? `有效至 ${formatTime(data.expires_at)}` : "永久有效",
    body: h(
      "div",
      { class: "stack" },
      notice("请立即复制并安全保存。关闭后无法再次查看完整密钥。", "warning", "triangle-alert"),
      secretBox("访问密钥", data.token),
      secretBox("播放列表地址", data.playlist_url),
    ),
    actions: [
      button("复制播放地址并关闭", {
        kind: "primary",
        iconName: "copy",
        onClick: async () => {
          await copyText(data.playlist_url, "播放列表地址已复制");
          closeModal();
          await ctx.reload();
        },
      }),
    ],
    onClose: () => ctx.reload(),
  });
}

function secretBox(label, value) {
  return h(
    "div",
    { class: "secret" },
    h("strong", { text: label }),
    h("code", { class: "secret-value mono", text: value }),
    button("复制", { size: "small", iconName: "copy", onClick: () => copyText(value, `${label}已复制`) }),
  );
}

async function revoke(ctx, token) {
  const accepted = await confirmDialog({
    title: "停用访问密钥？",
    description: "所有使用该密钥的播放器会立即失去访问权限。",
    warning: "停用后无法重新启用，需要重新创建一个密钥。",
    confirmLabel: "停用",
  });
  if (!accepted) return;
  try {
    await endpoints.revokeToken(token.id, token.revision);
    toast("访问密钥已停用");
    await ctx.reload();
  } catch (error) {
    toastError(error, "停用失败");
  }
}

async function remove(ctx, token) {
  const accepted = await confirmDialog({
    title: "删除访问密钥记录？",
    description: "删除记录不会代替停用操作，并会减少可用的审计信息。",
    warning: "如果该密钥仍然有效，请先停用再删除。",
    confirmLabel: "永久删除",
  });
  if (!accepted) return;
  try {
    await endpoints.deleteToken(token.id, token.revision);
    toast("记录已删除");
    await ctx.reload();
  } catch (error) {
    toastError(error, "删除失败");
  }
}
