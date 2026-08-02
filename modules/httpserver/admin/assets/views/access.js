import { formatTime, frag, h } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { loadCatalog, store } from "/admin/assets/core/store.js";
import { vt } from "/admin/assets/core/view-i18n.js";
import { badge, button, card, emptyState, field, input, notice, pageHead, select, table } from "/admin/assets/ui/kit.js";
import { closeModal, confirmDialog, copyText, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";

const LOG_LIMIT = 100;

export async function renderAccess(ctx) {
  const tab = ctx.query.get("tab") === "logs" ? "logs" : "tokens";
  const tabs = h(
    "div",
    { class: "tabs", role: "tablist" },
    h("button", { type: "button", role: "tab", "aria-selected": String(tab === "tokens"), text: vt("access.tokens"), onClick: () => ctx.navigate("/admin/access") }),
    h("button", { type: "button", role: "tab", "aria-selected": String(tab === "logs"), text: vt("access.logs"), onClick: () => ctx.navigate("/admin/access?tab=logs") }),
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
        [vt("access.name"), vt("access.prefix"), vt("access.scope"), vt("access.expires"), vt("access.status"), ""],
        tokens.map((token) => {
          const active = isActive(token);
          return h(
            "tr",
            {},
            h("td", {}, h("strong", { text: token.name }), token.note ? h("div", { class: "muted", text: token.note }) : null),
            h("td", { class: "mono", text: `${token.token_prefix}…` }),
            h("td", { class: "muted truncate", text: scopeLabel(token.scope) }),
            h("td", { class: "muted", text: token.expires_at ? formatTime(token.expires_at) : vt("access.noExpiry") }),
            h("td", {}, active ? badge(vt("access.valid"), "success") : badge(vt(token.revoked_at ? "access.revoked" : "access.expired"), "danger")),
            h(
              "td",
              {},
              h(
                "div",
                { class: "row-actions" },
                active ? button(vt("common.disable"), { kind: "danger", size: "small", onClick: () => revoke(ctx, token) }) : null,
                button(vt("common.delete"), { kind: "ghost", size: "small", onClick: () => remove(ctx, token) }),
              ),
            ),
          );
        }),
      )
    : emptyState(vt("access.emptyTokens"), vt("access.emptyTokensHint"), button(vt("access.create"), { kind: "primary", iconName: "plus", onClick: () => openCreateModal(ctx) }));

  return [
    pageHead(vt("access.title"), vt("access.tokensDesc"), [
      button(vt("access.create"), { kind: "primary", iconName: "plus", onClick: () => openCreateModal(ctx) }),
    ]),
    tabs,
    card({ title: vt("access.tokens"), description: vt("common.records", { count: tokens.length }), body, flush: true }),
  ];
}

async function renderLogs(ctx, tabs) {
  const data = await endpoints.accessLogs(LOG_LIMIT, ctx.signal);
  if (!ctx.alive()) return [];
  const logs = data.access_logs || [];

  const body = logs.length
    ? table(
        [vt("access.time"), vt("access.prefix"), vt("access.path"), vt("access.channel"), vt("access.status"), vt("access.remote")],
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
    : emptyState(vt("access.emptyLogs"), vt("access.emptyLogsHint"));

  const clear = async () => {
    const accepted = await confirmDialog({
      title: vt("access.clearTitle"),
      description: vt("access.clearDesc"),
      warning: vt("access.clearWarning"),
      confirmLabel: vt("access.clear"),
    });
    if (!accepted) return;
    try {
      const result = await endpoints.clearAccessLogs();
      toast(vt("access.cleared"), vt("access.deletedCount", { count: result.deleted || 0 }));
      await ctx.reload();
    } catch (error) {
      toastError(error, vt("access.clearFailed"));
    }
  };

  return [
    pageHead(vt("access.title"), vt("access.logsDesc"), [button(vt("access.clear"), { kind: "danger", iconName: "trash-2", onClick: clear })]),
    tabs,
    card({ title: vt("access.logs"), description: vt("access.logsSummary", { count: logs.length }), body, flush: true }),
  ];
}

function isActive(token) {
  return token.enabled && !token.revoked_at && (!token.expires_at || token.expires_at > Date.now() / 1000);
}

function scopeLabel(raw) {
  const value = String(raw || "").trim();
  if (!value || value === "all") return vt("access.allChannels");
  try {
    const ids = JSON.parse(value);
    if (!Array.isArray(ids) || !ids.length) return vt("access.noChannels");
    return ids.join(vt("common.listSeparator"));
  } catch {
    return value;
  }
}

async function openCreateModal(ctx) {
  await loadCatalog({});

  const nameInput = input("name", "", { required: true, placeholder: vt("access.namePlaceholder") });
  const noteInput = input("note", "", { placeholder: vt("access.notePlaceholder") });
  const expiry = select("expires_in_sec", [
    ["86400", vt("access.oneDay")],
    ["604800", vt("access.sevenDays")],
    ["2592000", vt("access.thirtyDays")],
    ["0", vt("access.noExpiry")],
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

  const create = button(vt("access.create"), {
    kind: "primary",
    onClick: async () => {
      if (!nameInput.value.trim()) {
        nameInput.setAttribute("aria-invalid", "true");
        toast(vt("access.nameRequired"), vt("access.nameRequiredHint"), "danger");
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
        toastError(error, vt("access.createFailed"));
      }
    },
  });

  openModal({
    title: vt("access.createTitle"),
    description: vt("access.createDesc"),
    body: h(
      "div",
      { class: "stack" },
      field(vt("access.name"), nameInput),
      field(vt("access.note"), noteInput),
      field(vt("access.validity"), expiry),
      h(
        "div",
        { class: "field" },
        h("span", { class: "field-label", text: vt("access.scope") }),
        h("label", { class: "check-row" }, allToggle, h("span", { text: vt("access.allowAll") })),
        h("div", { class: "check-list" }, channelChecks),
      ),
    ),
    actions: [button(vt("common.cancel"), { onClick: closeModal }), create],
  });
}

function showSecret(ctx, data) {
  openModal({
    title: vt("access.created"),
    description: data.expires_at ? vt("access.validUntil", { time: formatTime(data.expires_at) }) : vt("access.noExpiry"),
    body: h(
      "div",
      { class: "stack" },
      notice(vt("access.saveNow"), "warning", "triangle-alert"),
      secretBox(vt("access.tokens"), data.token),
      secretBox(vt("access.playlistURL"), data.playlist_url),
    ),
    actions: [
      button(vt("access.copyClose"), {
        kind: "primary",
        iconName: "copy",
        onClick: async () => {
          await copyText(data.playlist_url, vt("access.playlistCopied"));
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
    button(vt("common.copy"), { size: "small", iconName: "copy", onClick: () => copyText(value, vt("access.labelCopied", { label })) }),
  );
}

async function revoke(ctx, token) {
  const accepted = await confirmDialog({
    title: vt("access.revokeTitle"),
    description: vt("access.revokeDesc"),
    warning: vt("access.revokeWarning"),
    confirmLabel: vt("access.revokeAction"),
  });
  if (!accepted) return;
  try {
    await endpoints.revokeToken(token.id, token.revision);
    toast(vt("access.revokedToast"));
    await ctx.reload();
  } catch (error) {
    toastError(error, vt("access.revokeFailed"));
  }
}

async function remove(ctx, token) {
  const accepted = await confirmDialog({
    title: vt("access.removeTitle"),
    description: vt("access.removeDesc"),
    warning: vt("access.removeWarning"),
    confirmLabel: vt("channel.deleteForever"),
  });
  if (!accepted) return;
  try {
    await endpoints.deleteToken(token.id, token.revision);
    toast(vt("access.removed"));
    await ctx.reload();
  } catch (error) {
    toastError(error, vt("access.removeFailed"));
  }
}
