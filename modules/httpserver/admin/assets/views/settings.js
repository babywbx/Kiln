import { frag, h } from "/admin/assets/core/dom.js";
import { endpoints, remembersSession, saveSession } from "/admin/assets/core/api.js";
import { LOCALE_OPTIONS, i18n, localeLabel } from "/admin/assets/core/i18n.js";
import { store } from "/admin/assets/core/store.js";
import { badge, button, card, field, input, notice, pageHead, select, setBusy } from "/admin/assets/ui/kit.js";
import { closeModal, openModal, toast } from "/admin/assets/ui/overlay.js";
import { adminAPITokensCard } from "/admin/assets/views/api-tokens.js";

const passwordEncoder = new TextEncoder();
const controlCharacter = /\p{Cc}/u;

let settingsActions = {
  onAccountUpdated: async () => {},
  onLocaleChanged: async (locale) => {
    i18n.setLocale(locale);
    return true;
  },
};

export function configureSettingsActions(actions = {}) {
  settingsActions = { ...settingsActions, ...actions };
}

export async function renderSettings(ctx) {
  const [data, apiTokens, apiTokenLogs] = await Promise.all([
    endpoints.settings(ctx.signal),
    endpoints.adminAPITokens(ctx.signal),
    endpoints.adminAPITokenLogs(ctx.signal),
  ]);
  if (!ctx.alive()) return frag();

  const baseInput = input("public_base_url", data.public_base_url || "", { type: "url", placeholder: "https://kiln.example.com" });
  const retentionInput = input("access_log_retention_days", data.access_log_retention_days || "30", { type: "number", min: 1, max: 3650 });

  const saveButton = button(i18n.t("settings.save"), { kind: "primary", iconName: "check", disabled: true });
  const touch = () => {
    ctx.markDirty(true);
    saveButton.disabled = false;
  };
  baseInput.addEventListener("input", touch);
  retentionInput.addEventListener("input", touch);

  saveButton.addEventListener("click", async () => {
    setBusy(saveButton, true);
    try {
      await endpoints.saveSettings(
        {
          public_base_url: baseInput.value.trim(),
          access_log_retention_days: String(retentionInput.value).trim(),
        },
        data.revision,
      );
      ctx.markDirty(false);
      toast(i18n.t("settings.saved"));
      await ctx.reload();
    } catch (error) {
      toast(i18n.t("error.saveFailed"), errorDetail(error), "danger");
      setBusy(saveButton, false);
    }
  });

  const account = card({
    title: i18n.t("account.cardTitle"),
    description: i18n.t("account.cardDescription"),
    body: h(
      "div",
      { class: "list" },
      settingActionRow(
        i18n.t("account.usernameRow"),
        store.me?.username || "—",
        button(i18n.t("account.editAction"), { size: "small", iconName: "user-round-cog", onClick: openAccountSettings }),
        "accountUsername",
      ),
      settingActionRow(
        i18n.t("account.languageRow"),
        localeLabel(i18n.locale),
        button(i18n.t("account.languageAction"), { size: "small", iconName: "languages", onClick: openLanguageSettings }),
      ),
    ),
  });

  const runtime = h(
    "div",
    { class: "list" },
    runtimeRow(i18n.t("settings.listenAddress"), data.listen || "—"),
    runtimeRow(
      i18n.t("settings.playbackAuth"),
      null,
      data.play_require_auth ? badge(i18n.t("shared.enabled"), "success") : badge(i18n.t("shared.disabled"), "warning"),
    ),
    runtimeRow(i18n.t("settings.corsOrigins"), (data.cors_origins || []).join(" · ") || i18n.t("shared.sameOrigin")),
    runtimeRow(i18n.t("settings.publicHosts"), (data.public_hosts || []).join(" · ") || i18n.t("shared.unrestricted")),
    runtimeRow(i18n.t("settings.serviceVersion"), store.version || "—"),
  );

  return frag(
    pageHead(i18n.t("settings.title"), i18n.t("settings.description"), [saveButton]),
    h(
      "div",
      { class: "stack" },
      account,
      adminAPITokensCard(ctx, apiTokens, apiTokenLogs, data.public_base_url),
      h(
        "div",
        { class: "split" },
        card({
          title: i18n.t("settings.linksTitle"),
          body: h(
            "div",
            { class: "stack" },
            field(i18n.t("settings.publicBaseURL"), baseInput, i18n.t("settings.publicBaseURLHint")),
            field(i18n.t("settings.retentionDays"), retentionInput, i18n.t("settings.retentionDaysHint")),
          ),
        }),
        card({
          title: i18n.t("settings.runtimeTitle"),
          description: i18n.t("settings.runtimeDescription"),
          body: runtime,
        }),
      ),
    ),
  );
}

export function openAccountSettings() {
  const originalUsername = store.me?.username || "";
  const username = h("input", {
    id: "account-username",
    name: "username",
    value: originalUsername,
    autocomplete: "username",
    autofocus: true,
  });
  const currentPassword = h("input", {
    id: "account-current-password",
    name: "current_password",
    type: "password",
    autocomplete: "current-password",
  });
  const newPassword = h("input", {
    id: "account-new-password",
    name: "new_password",
    type: "password",
    autocomplete: "new-password",
  });
  const confirmPassword = h("input", {
    id: "account-confirm-password",
    name: "confirm_password",
    type: "password",
    autocomplete: "new-password",
  });

  const controls = { username, currentPassword, newPassword, confirmPassword };
  const errors = Object.fromEntries(Object.keys(controls).map((name) => [name, h("p", { class: "field-error", role: "status" })]));
  const formError = h("p", { class: "form-error", role: "alert", "aria-atomic": "true" });
  const save = button(i18n.t("account.save"), { kind: "primary", type: "submit", disabled: true });
  const saveLabel = save.querySelector("span");
  const touched = new Set();
  const serverErrors = { username: "", currentPassword: "", newPassword: "", confirmPassword: "", form: "" };
  let submitting = false;

  const validate = () => validateAccountChange(
    {
      username: username.value,
      currentPassword: currentPassword.value,
      newPassword: newPassword.value,
      confirmPassword: confirmPassword.value,
    },
    originalUsername,
  );

  const paint = (showAll = false) => {
    const state = validate();
    for (const [name, control] of Object.entries(controls)) {
      const errorKey = serverErrors[name] || state.errors[name];
      const visible = Boolean(errorKey && (showAll || touched.has(name)));
      errors[name].textContent = visible ? i18n.t(errorKey) : "";
      control.toggleAttribute("aria-invalid", visible);
      control.disabled = submitting;
    }
    formError.textContent = serverErrors.form ? i18n.t(serverErrors.form) : "";
    save.disabled = submitting || !state.valid;
    saveLabel.textContent = i18n.t(submitting ? "account.saving" : "account.save");
    return state;
  };

  const accountField = (name, label, control, hint = "") => {
    const errorId = `${control.id}-error`;
    errors[name].id = errorId;
    control.setAttribute("aria-describedby", [hint && `${control.id}-hint`, errorId].filter(Boolean).join(" "));
    return h(
      "div",
      { class: "field" },
      h("label", { class: "field-label", htmlFor: control.id, text: label }),
      control,
      hint ? h("p", { class: "field-hint", id: `${control.id}-hint`, text: hint }) : null,
      errors[name],
    );
  };

  for (const [name, control] of Object.entries(controls)) {
    control.addEventListener("blur", () => {
      touched.add(name);
      paint();
    });
    control.addEventListener("input", () => {
      serverErrors[name] = "";
      serverErrors.form = "";
      paint();
    });
  }

  const form = h(
    "form",
    { class: "stack", novalidate: true },
    accountField("username", i18n.t("account.username"), username, i18n.t("account.usernameHint")),
    accountField("currentPassword", i18n.t("account.currentPassword"), currentPassword),
    accountField("newPassword", i18n.t("account.newPassword"), newPassword, i18n.t("account.newPasswordHint")),
    accountField("confirmPassword", i18n.t("account.confirmPassword"), confirmPassword),
    notice(i18n.t("account.securityNotice"), "info", "shield-check"),
    formError,
  );
  form.id = "account-settings-form";
  save.setAttribute("form", form.id);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (submitting) return;
    const state = paint(true);
    if (!state.valid) {
      const first = Object.keys(controls).find((name) => state.errors[name]);
      controls[first]?.focus();
      return;
    }

    submitting = true;
    paint(true);
    const remember = remembersSession();
    try {
      const result = await endpoints.updateCredentials({
        username: state.username,
        current_password: currentPassword.value,
        new_password: newPassword.value,
      });
      saveSession(result.token, result.expires_at, remember);
      store.me = { ...store.me, username: result.username, role: result.role || store.me?.role };
      closeModal();
      await settingsActions.onAccountUpdated(result);
      toast(i18n.t("account.saved"));
    } catch (error) {
      const failure = classifyAccountError(error);
      if (failure.field) {
        touched.add(failure.field);
        serverErrors[failure.field] = failure.key;
        controls[failure.field].focus();
        controls[failure.field].select();
      } else {
        serverErrors.form = failure.key;
      }
    } finally {
      submitting = false;
      paint();
    }
  });

  openModal({
    title: i18n.t("account.modalTitle"),
    description: i18n.t("account.modalDescription"),
    body: form,
    actions: [button(i18n.t("shared.cancel"), { onClick: closeModal }), save],
  });
  requestAnimationFrame(() => username.focus());
  paint();
}

export function openLanguageSettings() {
  const language = select(
    "locale",
    LOCALE_OPTIONS.map((option) => [option.value, option.label]),
    i18n.locale,
  );
  const apply = button(i18n.t("shared.apply"), { kind: "primary", disabled: true });
  language.addEventListener("change", () => {
    apply.disabled = language.value === i18n.locale;
  });
  apply.addEventListener("click", async () => {
    if (language.value === i18n.locale) return;
    const applied = await settingsActions.onLocaleChanged(language.value);
    if (applied === false) {
      openLanguageSettings();
      return;
    }
    closeModal();
  });

  openModal({
    title: i18n.t("language.modalTitle"),
    description: i18n.t("language.modalDescription"),
    body: field(i18n.t("language.field"), language, i18n.t("language.hint")),
    actions: [button(i18n.t("shared.cancel"), { onClick: closeModal }), apply],
  });
  requestAnimationFrame(() => language.focus());
}

export function validateAccountChange(values, originalUsername) {
  const rawUsername = String(values.username || "");
  const username = rawUsername.trim();
  const currentPassword = String(values.currentPassword || "");
  const newPassword = String(values.newPassword || "");
  const confirmPassword = String(values.confirmPassword || "");
  const errors = { username: "", currentPassword: "", newPassword: "", confirmPassword: "" };
  const usernameLength = [...username].length;

  if (!username) errors.username = "account.error.usernameRequired";
  else if (usernameLength > 64) errors.username = "account.error.usernameLength";
  else if (controlCharacter.test(rawUsername)) errors.username = "account.error.usernameControl";
  if (!currentPassword) errors.currentPassword = "account.error.currentPasswordRequired";
  const passwordBytes = passwordEncoder.encode(newPassword).byteLength;
  if (newPassword && (passwordBytes < 8 || passwordBytes > 72)) errors.newPassword = "account.error.passwordLength";
  if (newPassword !== confirmPassword) errors.confirmPassword = "account.error.passwordMismatch";

  const changed = username !== originalUsername || Boolean(newPassword);
  return {
    username,
    errors,
    changed,
    valid: changed && !Object.values(errors).some(Boolean),
  };
}

function classifyAccountError(error) {
  if (error?.code === "current_password_invalid") return { key: "account.error.currentPasswordInvalid", field: "currentPassword" };
  if (error?.code === "username_taken") return { key: "account.error.usernameTaken", field: "username" };
  if (error?.code === "conflict" || error?.status === 409) return { key: "account.error.conflict", field: "" };
  if (error?.code === "too_many_requests" || error?.status === 429) return { key: "account.error.rateLimited", field: "" };
  if (error?.code === "invalid_request" || error?.status === 400 || error?.status === 422) {
    return { key: "account.error.invalidRequest", field: "" };
  }
  return { key: "account.error.unavailable", field: "" };
}

function errorDetail(error) {
  return error?.requestId ? i18n.t("shared.requestId", { id: error.requestId }) : i18n.t("error.tryAgain");
}

function settingActionRow(label, value, action, marker = "") {
  const valueAttrs = { class: "muted", text: value };
  if (marker) valueAttrs.dataset = { [marker]: "" };
  return h(
    "div",
    { class: "list-item" },
    h("span", {}, h("strong", { text: label }), h("small", valueAttrs)),
    action,
  );
}

function runtimeRow(label, value, node = null) {
  return h(
    "div",
    { class: "list-item" },
    h("span", {}, h("strong", { text: label })),
    node || h("span", { class: "mono muted truncate", text: value }),
  );
}
