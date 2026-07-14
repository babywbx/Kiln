import { lucideIcons } from "/admin/assets/third_party/lucide-icons.mjs";
import { viewLocale, vt } from "/admin/assets/core/view-i18n.js";

const SVG_NS = "http://www.w3.org/2000/svg";

export function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  applyAttrs(el, attrs);
  append(el, children);
  return el;
}

function applyAttrs(el, attrs) {
  for (const [key, value] of Object.entries(attrs)) {
    if (value == null || value === false) continue;
    if (key === "class") el.className = value;
    else if (key === "text") el.textContent = String(value);
    else if (key === "dataset") Object.assign(el.dataset, value);
    else if (key === "style") Object.assign(el.style, value);
    else if (key.startsWith("on") && typeof value === "function") el.addEventListener(key.slice(2).toLowerCase(), value);
    else if (key === "htmlFor") el.htmlFor = value;
    else el.setAttribute(key, value === true ? "" : String(value));
  }
}

function append(el, children) {
  for (const child of children) {
    if (child == null || child === false || child === "") continue;
    if (Array.isArray(child)) append(el, child);
    else el.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
}

export function frag(...children) {
  const f = document.createDocumentFragment();
  append(f, children);
  return f;
}

const iconTemplates = new Map();

function iconTemplate(name) {
  let svg = iconTemplates.get(name);
  if (!svg) {
    svg = document.createElementNS(SVG_NS, "svg");
    svg.setAttribute("class", "icon");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("fill", "none");
    svg.setAttribute("stroke", "currentColor");
    svg.setAttribute("stroke-width", "2");
    svg.setAttribute("stroke-linecap", "round");
    svg.setAttribute("stroke-linejoin", "round");
    svg.setAttribute("aria-hidden", "true");
    svg.innerHTML = lucideIcons[name] || "";
    iconTemplates.set(name, svg);
  }
  return svg;
}

export function icon(name, size = 20) {
  const svg = iconTemplate(name).cloneNode(true);
  svg.setAttribute("width", String(size));
  svg.setAttribute("height", String(size));
  return svg;
}

const numberFormats = new Map();
const dateFormats = new Map();
const clockFormats = new Map();

function localeFormatter(cache, create) {
  const locale = viewLocale();
  if (!cache.has(locale)) cache.set(locale, create(locale));
  return cache.get(locale);
}

export function formatNumber(value) {
  return localeFormatter(numberFormats, (locale) => new Intl.NumberFormat(locale)).format(Number(value || 0));
}

export function formatBytes(value) {
  const bytes = Number(value || 0);
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let amount = bytes;
  let unit = -1;
  do {
    amount /= 1024;
    unit += 1;
  } while (amount >= 1024 && unit < units.length - 1);
  return `${amount >= 100 ? amount.toFixed(0) : amount.toFixed(1)} ${units[unit]}`;
}

export function formatDuration(seconds) {
  const total = Math.max(0, Math.floor(Number(seconds) || 0));
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  if (days) return vt("common.duration.days", { days, hours });
  if (hours) return vt("common.duration.hours", { hours, minutes });
  if (minutes) return vt("common.duration.minutes", { minutes, seconds: total % 60 });
  return vt("common.duration.seconds", { seconds: total });
}

export function formatTime(seconds) {
  if (!seconds) return vt("common.never");
  return localeFormatter(dateFormats, (locale) => new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" })).format(new Date(Number(seconds) * 1000));
}

export function formatISOTime(value) {
  const stamp = Date.parse(value || "");
  if (!Number.isFinite(stamp) || stamp <= 0) return vt("common.never");
  return localeFormatter(dateFormats, (locale) => new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" })).format(new Date(stamp));
}

export function formatClock(date = new Date()) {
  return localeFormatter(clockFormats, (locale) => new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit" })).format(date);
}

export function initials(text) {
  return String(text || "?").trim().slice(0, 1).toUpperCase();
}
