import { h, icon } from "/admin/assets/core/dom.js";
import { endpoints, remembersSession, saveSession } from "/admin/assets/core/api.js";
import { store } from "/admin/assets/core/store.js";
import { button } from "/admin/assets/ui/kit.js";

export function renderLogin(onSuccess) {
  const username = h("input", { id: "login-user", name: "username", autocomplete: "username", required: true });
  const password = h("input", { id: "login-password", name: "password", type: "password", autocomplete: "current-password", required: true });
  const remember = h("input", { id: "login-remember", type: "checkbox", checked: remembersSession() });
  const error = h("p", { class: "form-error", role: "alert" });
  const submit = button("登录", { kind: "primary", type: "submit", trailingIcon: "chevron-right" });
  submit.classList.add("button-wide");

  const form = h(
    "form",
    { class: "login-form", novalidate: true },
    h("div", { class: "field" }, h("label", { class: "field-label", htmlFor: "login-user", text: "用户名" }), username),
    h("div", { class: "field" }, h("label", { class: "field-label", htmlFor: "login-password", text: "密码" }), password),
    h("label", { class: "check-row", htmlFor: "login-remember" }, remember, h("span", { text: "保持登录状态" })),
    error,
    submit,
  );

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    error.textContent = "";
    submit.disabled = true;
    try {
      const result = await endpoints.login({ username: username.value.trim(), password: password.value });
      saveSession(result.token, result.expires_at, remember.checked);
      store.me = { username: result.username, role: result.role };
      await onSuccess();
    } catch (apiError) {
      error.textContent = apiError.message || "登录失败";
      password.value = "";
      password.focus();
    } finally {
      submit.disabled = false;
    }
  });

  const view = h(
    "main",
    { class: "login" },
    h("div", { class: "login-backdrop", "aria-hidden": "true" }),
    h(
      "div",
      { class: "login-panel" },
      h("div", { class: "brand" }, brandMark(), h("span", { class: "brand-name", text: "Kiln" })),
      h(
        "div",
        { class: "login-copy" },
        h("p", { class: "eyebrow", text: "KILN 管理" }),
        h("h1", { text: "登录管理控制台" }),
        h("p", { text: "管理频道、访问权限、网络出口与系统设置。" }),
      ),
      form,
      h("p", { class: "login-foot" }, icon("lock", 13), h("span", { text: `本页面不加载任何外部资源 · Kiln ${store.version || ""}`.trim() })),
    ),
  );

  requestAnimationFrame(() => username.focus());
  return view;
}

export function brandMark() {
  return h("span", { class: "brand-mark", "aria-hidden": "true" }, h("i"), h("i"), h("i"));
}
