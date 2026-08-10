import { formatTime, h } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { vt } from "/admin/assets/core/view-i18n.js";
import { badge, button, card, emptyState, field, input, notice, select, setBusy, table } from "/admin/assets/ui/kit.js";
import { closeModal, confirmDialog, copyText, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";

const SCOPES = ["read", "write", "delete", "refresh"];
const PRESETS = {
  readonly: ["read"],
  operator: ["read", "refresh"],
  manager: ["read", "write", "refresh"],
  full: [...SCOPES],
};

export function adminAPITokensCard(ctx, data, logData, publicBase) {
  const tokens = data?.tokens || [];
  const logs = logData?.logs || [];
  const body = tokens.length
    ? table(
        [vt("apiToken.name"), vt("apiToken.prefix"), vt("apiToken.permissions"), vt("apiToken.expires"), vt("apiToken.lastUsed"), vt("apiToken.status"), ""],
        tokens.map((token) => tokenRow(ctx, token, publicBase)),
      )
    : emptyState(vt("apiToken.empty"), vt("apiToken.emptyHint"), button(vt("apiToken.create"), { kind: "primary", iconName: "plus", onClick: () => openTokenEditor(ctx, null, publicBase) }));

  return h(
    "div",
    { class: "stack" },
    card({
      title: vt("apiToken.title"),
      description: vt("apiToken.description"),
      action: button(vt("apiToken.create"), { size: "small", iconName: "plus", onClick: () => openTokenEditor(ctx, null, publicBase) }),
      body,
      flush: true,
    }),
    h(
      "details",
      { class: "disclosure" },
      h("summary", {}, h("span", { text: vt("apiToken.audit") }), h("small", { text: vt("apiToken.auditCount", { count: logs.length }) })),
      h("div", { class: "disclosure-body" }, auditTable(logs)),
    ),
  );
}

function tokenRow(ctx, token, publicBase) {
  const active = token.enabled && !token.revoked_at && (!token.expires_at || token.expires_at > Date.now() / 1000);
  const expired = token.expires_at && token.expires_at <= Date.now() / 1000;
  return h(
    "tr",
    {},
    h("td", {}, h("strong", { text: token.name }), token.note ? h("div", { class: "muted", text: token.note }) : null),
    h("td", { class: "mono", text: `${token.token_prefix}…` }),
    h("td", { class: "muted", text: scopeSummary(token.scopes) }),
    h("td", { class: "muted", text: token.expires_at ? formatTime(token.expires_at) : vt("apiToken.never") }),
    h("td", { class: "muted", text: token.last_used_at ? formatTime(token.last_used_at) : vt("common.never") }),
    h("td", {}, active ? badge(vt("apiToken.active"), "success") : badge(vt(token.revoked_at ? "apiToken.revoked" : expired ? "apiToken.expired" : "apiToken.disabled"), "danger")),
    h(
      "td",
      {},
      h(
        "div",
        { class: "row-actions" },
        !token.revoked_at ? button(vt("apiToken.edit"), { size: "small", onClick: () => openTokenEditor(ctx, token, publicBase) }) : null,
        active ? button(vt("apiToken.rotate"), { size: "small", onClick: () => rotateToken(ctx, token, publicBase) }) : null,
        active ? button(vt("apiToken.revoke"), { kind: "danger", size: "small", onClick: () => revokeToken(ctx, token) }) : null,
        button(vt("common.delete"), { kind: "ghost", size: "small", onClick: () => deleteToken(ctx, token) }),
      ),
    ),
  );
}

function auditTable(logs) {
  if (!logs.length) return emptyState(vt("apiToken.auditEmpty"), vt("apiToken.auditEmptyHint"));
  return table(
    [vt("apiToken.time"), vt("apiToken.prefix"), vt("apiToken.request"), vt("apiToken.permission"), vt("apiToken.decision"), vt("apiToken.status")],
    logs.slice(0, 100).map((log) => h(
      "tr",
      {},
      h("td", { class: "muted", text: formatTime(log.created_at) }),
      h("td", { class: "mono", text: `${log.token_prefix || "—"}…` }),
      h("td", {}, h("span", { class: "mono", text: log.method }), h("div", { class: "mono muted truncate", text: log.path })),
      h("td", { text: scopeLabel(log.required_scope) }),
      h("td", {}, badge(vt(log.decision === "allow" ? "apiToken.allowed" : "apiToken.denied"), log.decision === "allow" ? "success" : "danger"), log.reason ? h("div", { class: "muted", text: log.reason }) : null),
      h("td", { text: String(log.status || "—") }),
    )),
  );
}

function openTokenEditor(ctx, existing, publicBase) {
  const name = input("name", existing?.name || "", { required: true, placeholder: vt("apiToken.namePlaceholder") });
  const note = input("note", existing?.note || "", { placeholder: vt("apiToken.notePlaceholder") });
  const expiry = select("expiry", [
    ...(existing ? [["keep", vt("apiToken.keepExpiry")]] : []),
    ["604800", vt("apiToken.sevenDays")],
    ["2592000", vt("apiToken.thirtyDays")],
    ["7776000", vt("apiToken.ninetyDays")],
    ["31536000", vt("apiToken.oneYear")],
    ["0", vt("apiToken.never")],
  ], existing ? "keep" : "7776000");
  const preset = select("preset", [
    ["readonly", vt("apiToken.presetReadonly")],
    ["operator", vt("apiToken.presetOperator")],
    ["manager", vt("apiToken.presetManager")],
    ["full", vt("apiToken.presetFull")],
    ["custom", vt("apiToken.presetCustom")],
  ], existing ? "custom" : "operator");
  const enabled = h("input", { type: "checkbox", checked: existing?.enabled !== false });
  const scopeChecks = Object.fromEntries(SCOPES.map((scope) => [scope, h("input", { type: "checkbox" })]));
  const setScopes = (values) => {
    for (const scope of SCOPES) scopeChecks[scope].checked = values.includes(scope);
  };
  setScopes(existing?.scopes || PRESETS.operator);
  preset.addEventListener("change", () => {
    if (preset.value !== "custom") setScopes(PRESETS[preset.value]);
  });
  for (const checkbox of Object.values(scopeChecks)) checkbox.addEventListener("change", () => { preset.value = "custom"; });

  const submit = button(vt(existing ? "apiToken.save" : "apiToken.create"), {
    kind: "primary",
    onClick: async () => {
      const scopes = SCOPES.filter((scope) => scopeChecks[scope].checked);
      if (!name.value.trim() || !scopes.length) {
        if (!name.value.trim()) name.setAttribute("aria-invalid", "true");
        toast(vt("apiToken.invalid"), vt("apiToken.invalidHint"), "danger");
        return;
      }
      setBusy(submit, true);
      try {
        if (existing) {
          await endpoints.updateAdminAPIToken(existing.id, {
            name: name.value.trim(), note: note.value.trim(), scopes, enabled: enabled.checked,
            expires_at: expiry.value === "keep"
              ? existing.expires_at || 0
              : Number(expiry.value) ? Math.floor(Date.now() / 1000) + Number(expiry.value) : 0,
          }, existing.revision);
          closeModal();
          toast(vt("apiToken.saved"));
          await ctx.reload();
        } else {
          const result = await endpoints.createAdminAPIToken({
            name: name.value.trim(), note: note.value.trim(), scopes,
            expires_in_sec: Number(expiry.value),
          });
          showIssuedToken(ctx, result, publicBase);
        }
      } catch (error) {
        toastError(error, vt("apiToken.saveFailed"));
        setBusy(submit, false);
      }
    },
  });

  openModal({
    title: vt(existing ? "apiToken.editTitle" : "apiToken.createTitle"),
    description: vt(existing ? "apiToken.editDescription" : "apiToken.createDescription"),
    body: h(
      "div",
      { class: "stack" },
      h("div", { class: "form-grid" }, field(vt("apiToken.name"), name), field(vt("apiToken.note"), note)),
      field(vt("apiToken.permissionPreset"), preset, vt("apiToken.permissionPresetHint")),
      h("div", { class: "check-list permission-grid" }, SCOPES.map((scope) => h(
        "label",
        { class: "check-row" },
        scopeChecks[scope],
        h("span", {}, h("strong", { text: scopeLabel(scope) }), h("small", { text: vt(`apiToken.scope.${scope}Hint`) })),
      ))),
      field(vt("apiToken.validity"), expiry),
      existing ? h("label", { class: "check-row" }, enabled, h("span", { text: vt("apiToken.enabled") })) : null,
      notice(vt("apiToken.securityNotice"), "info", "shield-check"),
    ),
    actions: [button(vt("common.cancel"), { onClick: closeModal }), submit],
  });
}

function showIssuedToken(ctx, result, publicBase) {
  const token = result.token;
  const base = String(publicBase || location.origin).replace(/\/$/, "");
  const curl = `curl -H ${shellQuote(`Authorization: Bearer ${token}`)} ${shellQuote(`${base}/v1/admin/channels`)}`;
  openModal({
    title: vt("apiToken.issuedTitle"),
    description: vt("apiToken.issuedDescription"),
    body: h(
      "div",
      { class: "stack" },
      notice(vt("apiToken.showOnce"), "warning", "triangle-alert"),
      secretBox(vt("apiToken.token"), token),
      secretBox(vt("apiToken.curl"), curl),
    ),
    actions: [button(vt("apiToken.copyClose"), {
      kind: "primary", iconName: "copy",
      onClick: async () => { await copyText(token, vt("apiToken.copied")); closeModal(); },
    })],
    onClose: () => ctx.reload(),
  });
}

function shellQuote(value) {
  return `'${String(value).replaceAll("'", `'"'"'`)}'`;
}

function secretBox(label, value) {
  return h(
    "div",
    { class: "secret" },
    h("strong", { text: label }),
    h("code", { class: "secret-value mono", text: value }),
    button(vt("common.copy"), { size: "small", iconName: "copy", onClick: () => copyText(value, vt("apiToken.copied")) }),
  );
}

async function rotateToken(ctx, token, publicBase) {
  if (!(await confirmDialog({ title: vt("apiToken.rotateTitle"), description: vt("apiToken.rotateDescription"), warning: vt("apiToken.rotateWarning"), confirmLabel: vt("apiToken.rotate"), tone: "warning" }))) return;
  try {
    const result = await endpoints.rotateAdminAPIToken(token.id, token.revision);
    showIssuedToken(ctx, result, publicBase);
  } catch (error) {
    toastError(error, vt("apiToken.rotateFailed"));
  }
}

async function revokeToken(ctx, token) {
  if (!(await confirmDialog({ title: vt("apiToken.revokeTitle"), description: vt("apiToken.revokeDescription"), warning: vt("apiToken.revokeWarning"), confirmLabel: vt("apiToken.revoke") }))) return;
  try {
    await endpoints.revokeAdminAPIToken(token.id, token.revision);
    toast(vt("apiToken.revokedToast"));
    await ctx.reload();
  } catch (error) {
    toastError(error, vt("apiToken.revokeFailed"));
  }
}

async function deleteToken(ctx, token) {
  if (!(await confirmDialog({ title: vt("apiToken.deleteTitle"), description: vt("apiToken.deleteDescription"), warning: vt("apiToken.deleteWarning"), confirmLabel: vt("common.delete") }))) return;
  try {
    await endpoints.deleteAdminAPIToken(token.id, token.revision);
    toast(vt("apiToken.deleted"));
    await ctx.reload();
  } catch (error) {
    toastError(error, vt("apiToken.deleteFailed"));
  }
}

function scopeLabel(scope) {
  return vt(`apiToken.scope.${scope}`);
}

function scopeSummary(scopes = []) {
  return scopes.map(scopeLabel).join(vt("common.listSeparator")) || "—";
}
