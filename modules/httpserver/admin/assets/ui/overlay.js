import { h, icon } from "/admin/assets/core/dom.js";
import { button } from "/admin/assets/ui/kit.js";

const TOAST_MS = 4200;

const toastIcons = { success: "circle-check", danger: "circle-alert", warning: "triangle-alert", info: "info" };

export function toast(title, message = "", tone = "success") {
  const region = document.getElementById("toast-region");
  const item = h(
    "div",
    { class: `toast toast-${tone}`, role: tone === "danger" ? "alert" : "status" },
    icon(toastIcons[tone] || toastIcons.info, 18),
    h("div", { class: "toast-copy" }, h("strong", { text: title }), message ? h("p", { text: message }) : null),
  );
  region.append(item);
  const remove = () => {
    item.classList.add("is-leaving");
    item.addEventListener("animationend", () => item.remove(), { once: true });
  };
  setTimeout(remove, TOAST_MS);
  item.addEventListener("click", remove);
}

export function toastError(error, fallback = "操作失败") {
  const detail = [error?.message, error?.detail].filter(Boolean).join(" · ");
  toast(fallback, detail, "danger");
}

const dialog = () => document.getElementById("modal");

export function openModal({ title, description, body, actions = [], onClose }) {
  const el = dialog();
  const content = document.getElementById("modal-content");
  content.replaceChildren(
    h(
      "div",
      { class: "modal-head" },
      h("div", { class: "modal-title" }, h("h2", { text: title }), description ? h("p", { text: description }) : null),
      h("button", { class: "icon-button", type: "button", "aria-label": "关闭", onClick: closeModal }, icon("x", 18)),
    ),
    h("div", { class: "modal-body" }, body),
    actions.length ? h("div", { class: "modal-actions" }, actions) : null,
  );
  el.returnValue = "";
  if (!el.open) el.showModal();
  if (onClose) el.addEventListener("close", onClose, { once: true });
  return el;
}

export function closeModal() {
  const el = dialog();
  if (el.open) el.close();
  document.getElementById("modal-content").replaceChildren();
}

export function confirmDialog({ title, description, confirmLabel, tone = "danger", expect = "", warning = "" }) {
  return new Promise((resolve) => {
    let settled = false;
    const settle = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
    };

    const confirmButton = button(confirmLabel, {
      kind: tone,
      disabled: Boolean(expect),
      onClick: () => {
        settle(true);
        closeModal();
      },
    });

    let field = null;
    if (expect) {
      const input = h("input", {
        type: "text",
        autocomplete: "off",
        placeholder: expect,
        "aria-label": "输入确认内容",
        onInput: (event) => {
          confirmButton.disabled = event.target.value.trim() !== expect;
        },
      });
      field = h(
        "label",
        { class: "field" },
        h("span", { class: "field-label", text: `输入 ${expect} 以确认` }),
        input,
      );
    }

    openModal({
      title,
      description,
      body: h(
        "div",
        { class: "stack" },
        warning ? h("div", { class: `notice notice-${tone}` }, icon("triangle-alert", 18), h("span", { text: warning })) : null,
        field,
      ),
      actions: [button("取消", { onClick: closeModal }), confirmButton],
      onClose: () => settle(false),
    });
  });
}

// Native popover gives light-dismiss (outside click + Escape) for free — the
// old hand-rolled menu leaked open state on every stray click.
export function attachMenu(anchor, items) {
  const menu = h(
    "div",
    { class: "menu", role: "menu", popover: "auto" },
    items.map((item) =>
      h(
        "button",
        {
          type: "button",
          role: "menuitem",
          class: item.tone === "danger" ? "menu-item is-danger" : "menu-item",
          onClick: () => {
            menu.hidePopover();
            item.onSelect();
          },
        },
        icon(item.icon, 16),
        h("span", { text: item.label }),
      ),
    ),
  );
  document.body.append(menu);

  anchor.setAttribute("aria-haspopup", "menu");
  anchor.setAttribute("aria-expanded", "false");
  anchor.addEventListener("click", () => {
    const box = anchor.getBoundingClientRect();
    menu.style.top = `${Math.round(box.bottom + 8)}px`;
    menu.style.right = `${Math.round(window.innerWidth - box.right)}px`;
    menu.togglePopover();
  });
  menu.addEventListener("toggle", (event) => {
    anchor.setAttribute("aria-expanded", String(event.newState === "open"));
  });

  return { close: () => menu.hidePopover() };
}

export async function copyText(value, message = "已复制") {
  try {
    await navigator.clipboard.writeText(value);
    toast(message);
  } catch {
    toast("复制失败", "浏览器拒绝了剪贴板访问，请手动复制。", "danger");
  }
}
