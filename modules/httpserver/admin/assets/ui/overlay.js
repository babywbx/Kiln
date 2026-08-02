import { h, icon } from "/admin/assets/core/dom.js";
import { vt } from "/admin/assets/core/view-i18n.js";
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

export function toastError(error, fallback = "") {
  const detail = [error?.message, error?.detail].filter(Boolean).join(" · ");
  toast(fallback || vt("common.actionFailed"), detail, "danger");
}

const dialog = () => document.getElementById("modal");

export function openModal({ title, description, body, actions = [], onClose }) {
  const el = dialog();
  const content = document.getElementById("modal-content");
  content.replaceChildren(
    h(
      "div",
      { class: "modal-head" },
      h(
        "div",
        { class: "modal-title" },
        h("h2", { id: "modal-title", text: title }),
        description ? h("p", { id: "modal-desc", text: description }) : null,
      ),
      h("button", { class: "icon-button", type: "button", "aria-label": vt("common.close"), onClick: closeModal }, icon("x", 18)),
    ),
    h("div", { class: "modal-body" }, body),
    actions.length ? h("div", { class: "modal-actions" }, actions) : null,
  );
  el.setAttribute("aria-labelledby", "modal-title");
  if (description) el.setAttribute("aria-describedby", "modal-desc");
  else el.removeAttribute("aria-describedby");
  el.returnValue = "";
  el.addEventListener("close", () => content.replaceChildren(), { once: true });
  if (onClose) el.addEventListener("close", onClose, { once: true });
  if (!el.open) el.showModal();
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
        autofocus: true,
        placeholder: expect,
        "aria-label": vt("overlay.confirmInput"),
        onInput: (event) => {
          confirmButton.disabled = event.target.value.trim() !== expect;
        },
      });
      field = h(
        "label",
        { class: "field" },
        h("span", { class: "field-label", text: vt("overlay.confirmLabel", { value: expect }) }),
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
      actions: [button(vt("common.cancel"), { onClick: closeModal }), confirmButton],
      onClose: () => settle(false),
    });
  });
}

let menuSeq = 0;

export function attachMenu(anchor, items) {
  const id = `menu-${++menuSeq}`;
  const menu = h(
    "div",
    { class: "menu", id, role: "menu", popover: "auto" },
    items.map((item) =>
      h(
        "button",
        {
          type: "button",
          role: "menuitem",
          tabindex: "-1",
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

  const entries = () => [...menu.querySelectorAll(".menu-item")];
  const dismiss = () => {
    menu.hidePopover();
    anchor.focus();
  };
  let landing = "first";

  anchor.setAttribute("popovertarget", id);
  anchor.setAttribute("aria-haspopup", "menu");
  anchor.setAttribute("aria-expanded", "false");

  anchor.addEventListener("keydown", (event) => {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    if (menu.matches(":popover-open")) return;
    event.preventDefault();
    landing = event.key === "ArrowUp" ? "last" : "first";
    menu.showPopover();
  });

  menu.addEventListener("beforetoggle", (event) => {
    if (event.newState !== "open") return;
    const box = anchor.getBoundingClientRect();
    menu.style.top = `${Math.round(box.bottom + 8)}px`;
    menu.style.right = `${Math.round(window.innerWidth - box.right)}px`;
  });

  menu.addEventListener("toggle", (event) => {
    const open = event.newState === "open";
    anchor.setAttribute("aria-expanded", String(open));
    if (open) {
      const all = entries();
      (landing === "last" ? all.at(-1) : all[0])?.focus();
    }
    landing = "first";
  });

  menu.addEventListener("keydown", (event) => {
    const all = entries();
    const at = all.indexOf(document.activeElement);
    const move = (next) => {
      event.preventDefault();
      all[(next + all.length) % all.length]?.focus();
    };
    if (event.key === "ArrowDown") move(at + 1);
    else if (event.key === "ArrowUp") move(at - 1);
    else if (event.key === "Home") move(0);
    else if (event.key === "End") move(all.length - 1);
    else if (event.key === "Escape" || event.key === "Tab") dismiss();
  });

  return { close: () => menu.hidePopover(), dispose: () => menu.remove() };
}

export async function copyText(value, message = "") {
  try {
    await navigator.clipboard.writeText(value);
    toast(message || vt("common.copied"));
  } catch {
    toast(vt("common.copyFailed"), vt("common.copyDenied"), "danger");
  }
}
