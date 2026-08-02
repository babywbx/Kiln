import { h, icon, initials } from "/admin/assets/core/dom.js";
import { vt } from "/admin/assets/core/view-i18n.js";

export function button(label, options = {}) {
  const { kind = "secondary", size = "", type = "button", onClick, disabled = false, iconName = "", trailingIcon = "" } = options;
  return h(
    "button",
    {
      class: `button button-${kind}${size ? ` button-${size}` : ""}`,
      type,
      disabled,
      onClick,
      "aria-label": options.ariaLabel,
    },
    iconName ? icon(iconName, 16) : null,
    label ? h("span", { text: label }) : null,
    trailingIcon ? icon(trailingIcon, 16) : null,
  );
}

export function linkButton(label, href, options = {}) {
  const { kind = "secondary", iconName = "" } = options;
  return h(
    "a",
    { class: `button button-${kind}`, href, "data-route": true, "aria-label": options.ariaLabel },
    iconName ? icon(iconName, 16) : null,
    h("span", { text: label }),
  );
}

export function iconButton(iconName, label, options = {}) {
  return h(
    "button",
    {
      class: `icon-button${options.kind === "danger" ? " is-danger" : ""}${options.variant === "outline" ? " is-outline" : ""}`,
      type: "button",
      disabled: options.disabled,
      title: label,
      "aria-label": label,
      onClick: options.onClick,
    },
    icon(iconName, options.size || 18),
  );
}

export function badge(label, tone = "neutral", iconName = "") {
  return h("span", { class: `badge badge-${tone}` }, iconName ? icon(iconName, 13) : null, h("span", { text: label }));
}

const STATE_BADGES = {
  running: ["state.running", "success"],
  starting: ["state.starting", "warning"],
  restarting: ["state.restarting", "warning"],
  failed: ["state.failed", "danger"],
};

export function sessionStateBadge(state) {
  const [label, tone] = STATE_BADGES[state] || [state || vt("common.unknown"), "neutral"];
  return badge(STATE_BADGES[state] ? vt(label) : label, tone);
}

export function stateBadge(channel, session) {
  if (channel?.disabled) return badge(vt("state.disabled"), "danger");
  if (!session) return badge(vt("state.idle"), "neutral");
  return sessionStateBadge(session.state);
}

export function runModeLabel(channel) {
  if (channel.autostart && channel.on_demand) return vt("run.prewarm");
  if (channel.autostart) return vt("run.persistent");
  return vt("run.ondemand");
}

export function pageHead(title, description, actions = []) {
  return h(
    "header",
    { class: "page-head" },
    h("div", { class: "page-head-copy" }, h("h1", { text: title }), description ? h("p", { text: description }) : null),
    actions.length ? h("div", { class: "page-head-actions" }, actions) : null,
  );
}

export function card(options = {}) {
  const { title, description, action, body, flush = false, tone = "" } = options;
  return h(
    "section",
    { class: `card${tone ? ` card-${tone}` : ""}` },
    title
      ? h(
          "div",
          { class: "card-head" },
          h("div", { class: "card-title" }, h("h2", { text: title }), description ? h("p", { text: description }) : null),
          action ? h("div", { class: "card-action" }, action) : null,
        )
      : null,
    h("div", { class: `card-body${flush ? " is-flush" : ""}` }, body),
  );
}

export function field(label, control, hint = "") {
  const id = control.id || `f-${crypto.randomUUID().slice(0, 8)}`;
  control.id = id;
  return h(
    "div",
    { class: "field" },
    h("label", { class: "field-label", htmlFor: id, text: label }),
    control,
    hint ? h("p", { class: "field-hint", text: hint }) : null,
  );
}

export function input(name, value = "", options = {}) {
  const el = h("input", { name, type: options.type || "text", value: value ?? "", autocomplete: "off" });
  if (options.placeholder) el.placeholder = options.placeholder;
  if (options.required) el.required = true;
  if (options.disabled) el.disabled = true;
  if (options.min != null) el.min = String(options.min);
  if (options.max != null) el.max = String(options.max);
  if (options.maxlength != null) el.maxLength = Number(options.maxlength);
  if (options.pattern) el.pattern = options.pattern;
  return el;
}

export function select(name, choices, value) {
  return h(
    "select",
    { name },
    choices.map(([choiceValue, label]) => h("option", { value: choiceValue, selected: choiceValue === value, text: label })),
  );
}

export function emptyState(title, description, action = null) {
  return h(
    "div",
    { class: "empty-state" },
    h("strong", { text: title }),
    description ? h("p", { text: description }) : null,
    action,
  );
}

export function table(headers, rows) {
  return h(
    "div",
    { class: "table-wrap" },
    h(
      "table",
      {},
      h("thead", {}, h("tr", {}, headers.map((label) => h("th", { text: label })))),
      h("tbody", {}, rows),
    ),
  );
}

const DIGIT_RE = /^[0-9]$/;

export function numberRoll() {
  const label = h("span", { class: "sr-only" });
  const track = h("span", { class: "roll-track", "aria-hidden": "true" });
  const node = h("span", { class: "roll" }, label, track);
  let slots = [];

  const digitSlot = () => {
    const reel = h("span", { class: "roll-reel" }, [..."0123456789"].map((step) => h("span", { class: "roll-step", text: step })));
    return { reel, cell: h("span", { class: "roll-cell" }, reel), value: -1 };
  };

  const charSlot = (ch) => ({ reel: null, cell: h("span", { class: "roll-cell", text: ch }), value: ch });

  const spin = (slot, digit) => {
    if (slot.value === digit) return;
    const fresh = slot.value === -1;
    slot.value = digit;
    if (fresh) {
      requestAnimationFrame(() => requestAnimationFrame(() => {
        slot.reel.style.transform = `translateY(${-slot.value}em)`;
      }));
    } else {
      slot.reel.style.transform = `translateY(${-digit}em)`;
    }
  };

  const set = (text) => {
    const value = String(text ?? "");
    label.textContent = value;
    const chars = [...value];

    if (chars.length !== slots.length) {
      slots = chars.map((ch) => (DIGIT_RE.test(ch) ? digitSlot() : charSlot(ch)));
      track.replaceChildren(...slots.map((slot) => slot.cell));
    } else {
      slots = slots.map((slot, index) => {
        const ch = chars[index];
        if (DIGIT_RE.test(ch) ? slot.reel : slot.value === ch) return slot;
        const next = DIGIT_RE.test(ch) ? digitSlot() : charSlot(ch);
        slot.cell.replaceWith(next.cell);
        return next;
      });
    }

    slots.forEach((slot, index) => {
      if (slot.reel) spin(slot, Number(chars[index]));
    });
  };

  return { node, set };
}

export function channelAvatar(channel, size = 38) {
  const box = h("span", { class: "avatar avatar-channel", style: { width: `${size}px`, height: `${size}px` } });
  box.textContent = initials(channel.title || channel.id);
  if (channel.logo_url) {
    const image = h("img", { src: channel.logo_url, alt: "", loading: "lazy" });
    image.addEventListener("load", () => box.replaceChildren(image), { once: true });
  }
  return box;
}

export function channelCell(channel) {
  return h(
    "div",
    { class: "identity" },
    channelAvatar(channel),
    h(
      "div",
      { class: "identity-copy" },
      h("strong", { text: channel.title || channel.id }),
      h("small", { class: "mono", text: channel.id }),
    ),
  );
}

export function notice(message, tone = "info", iconName = "info") {
  return h("div", { class: `notice notice-${tone}` }, icon(iconName, 18), h("span", { text: message }));
}

export function formSection(step, title, description, body) {
  return h(
    "section",
    { class: "form-section" },
    h(
      "div",
      { class: "form-section-head" },
      h("span", { class: "step", "aria-hidden": "true", text: step }),
      h("div", {}, h("h2", { text: title }), h("p", { text: description })),
    ),
    body,
  );
}
