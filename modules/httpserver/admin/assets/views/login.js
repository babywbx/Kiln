import { h } from "/admin/assets/core/dom.js";
import { endpoints, remembersSession, saveSession } from "/admin/assets/core/api.js";
import { LOCALE_OPTIONS } from "/admin/assets/core/i18n.js";
import { store } from "/admin/assets/core/store.js";
import { button } from "/admin/assets/ui/kit.js";
import { classifyLoginError, validateCredentials } from "/admin/assets/views/login-model.js";

export function renderLogin(onSuccess, options) {
  const { i18n, onLocaleChange } = options;
  const username = h("input", {
    id: "login-user",
    name: "username",
    autocomplete: "username",
    required: true,
    "aria-describedby": "login-user-error login-form-error",
  });
  const password = h("input", {
    id: "login-password",
    name: "password",
    type: "password",
    autocomplete: "current-password",
    required: true,
    "aria-describedby": "login-password-error login-form-error",
  });
  const remember = h("input", { id: "login-remember", type: "checkbox", checked: remembersSession() });
  const usernameLabel = h("label", { class: "field-label", htmlFor: "login-user" });
  const passwordLabel = h("label", { class: "field-label", htmlFor: "login-password" });
  const usernameError = h("p", { class: "field-error", id: "login-user-error" });
  const passwordError = h("p", { class: "field-error", id: "login-password-error" });
  const rememberLabel = h("span");
  const error = h("p", { class: "form-error", id: "login-form-error", role: "alert", "aria-atomic": "true" });
  const submit = button(i18n.t("login.submit"), { kind: "primary", type: "submit", trailingIcon: "chevron-right" });
  submit.classList.add("button-wide");
  const submitLabel = submit.querySelector("span");

  const language = h(
    "select",
    { class: "login-language-select", id: "login-language" },
    LOCALE_OPTIONS.map(({ value, label }) => h("option", { value, text: label })),
  );
  language.value = i18n.locale;

  const eyebrow = h("p", { class: "eyebrow" });
  const title = h("h1");
  const description = h("p");
  let usernameErrorKey = "";
  let passwordErrorKey = "";
  let formErrorKey = "";
  let submitting = false;

  const form = h(
    "form",
    { class: "login-form", novalidate: true },
    h("div", { class: "field" }, usernameLabel, username, usernameError),
    h("div", { class: "field" }, passwordLabel, password, passwordError),
    h("label", { class: "check-row", htmlFor: "login-remember" }, remember, rememberLabel),
    error,
    submit,
  );

  function paint() {
    language.setAttribute("aria-label", i18n.t("login.language"));
    eyebrow.textContent = i18n.t("login.eyebrow");
    title.textContent = i18n.t("login.title");
    description.textContent = i18n.t("login.description");
    usernameLabel.textContent = i18n.t("login.username");
    passwordLabel.textContent = i18n.t("login.password");
    rememberLabel.textContent = i18n.t("login.remember");
    usernameError.textContent = usernameErrorKey ? i18n.t(usernameErrorKey) : "";
    passwordError.textContent = passwordErrorKey ? i18n.t(passwordErrorKey) : "";
    error.textContent = formErrorKey ? i18n.t(formErrorKey) : "";
    submitLabel.textContent = i18n.t(submitting ? "login.submitting" : "login.submit");
    submit.disabled = submitting;
    form.setAttribute("aria-busy", String(submitting));
  }

  function clearErrors() {
    usernameErrorKey = "";
    passwordErrorKey = "";
    formErrorKey = "";
    username.removeAttribute("aria-invalid");
    password.removeAttribute("aria-invalid");
  }

  function showValidation(validation) {
    usernameErrorKey = validation.usernameError;
    passwordErrorKey = validation.passwordError;
    if (usernameErrorKey) username.setAttribute("aria-invalid", "true");
    if (passwordErrorKey) password.setAttribute("aria-invalid", "true");
    paint();
    (validation.focus === "username" ? username : password).focus();
  }

  function clearErrorFor(field) {
    if (formErrorKey) {
      formErrorKey = "";
      username.removeAttribute("aria-invalid");
      password.removeAttribute("aria-invalid");
    }
    if (field === "username") {
      usernameErrorKey = "";
      username.removeAttribute("aria-invalid");
    } else {
      passwordErrorKey = "";
      password.removeAttribute("aria-invalid");
    }
    paint();
  }

  username.addEventListener("input", () => clearErrorFor("username"));
  password.addEventListener("input", () => clearErrorFor("password"));
  language.addEventListener("change", () => {
    i18n.setLocale(language.value);
    paint();
    onLocaleChange();
  });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (submitting) return;
    clearErrors();
    const validation = validateCredentials(username.value, password.value);
    if (validation) {
      showValidation(validation);
      return;
    }

    submitting = true;
    paint();
    try {
      const result = await endpoints.login({ username: username.value.trim(), password: password.value });
      if (result.role !== "admin") {
        formErrorKey = "login.error.adminRequired";
        password.value = "";
        requestAnimationFrame(() => username.focus());
        return;
      }
      saveSession(result.token, result.expires_at, remember.checked);
      store.me = { username: result.username, role: result.role };
      await onSuccess();
    } catch (apiError) {
      const state = classifyLoginError(apiError);
      formErrorKey = state.key;
      if (state.clearPassword) password.value = "";
      for (const field of state.invalidFields) (field === "username" ? username : password).setAttribute("aria-invalid", "true");
      if (state.focus) requestAnimationFrame(() => (state.focus === "username" ? username : password).focus());
    } finally {
      submitting = false;
      paint();
    }
  });

  const view = h(
    "main",
    { class: "login", id: "main-content", tabindex: "-1" },
    h("div", { class: "login-backdrop", "aria-hidden": "true" }),
    h(
      "div",
      { class: "login-panel" },
      h(
        "div",
        { class: "login-head" },
        h("div", { class: "brand" }, brandMark(), h("span", { class: "brand-name", text: "Kiln" })),
        language,
      ),
      h(
        "div",
        { class: "login-copy" },
        eyebrow,
        title,
        description,
      ),
      form,
      h("p", { class: "login-foot", text: `Kiln ${store.version || ""}`.trim() }),
    ),
  );

  paint();
  requestAnimationFrame(() => username.focus());
  return view;
}

export function brandMark() {
  return h("span", { class: "brand-mark", "aria-hidden": "true" }, h("i"), h("i"), h("i"));
}
