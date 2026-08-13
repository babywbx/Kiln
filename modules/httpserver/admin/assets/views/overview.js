import { formatClock, formatNumber, frag, h } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { i18n } from "/admin/assets/core/i18n.js";
import { errorSummary } from "/admin/assets/core/session-error.js";
import { loadCatalog, refreshStatus, store } from "/admin/assets/core/store.js";
import { vt } from "/admin/assets/core/view-i18n.js";
import { badge, button, card, emptyState, linkButton, numberRoll, pageHead, table } from "/admin/assets/ui/kit.js";
import { closeModal, confirmDialog, copyButton, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";

const ENGINE_LABELS = {
  native_rewrite: "overview.engine.nativeRewrite",
  native_remux: "overview.engine.nativeRemux",
  ffmpeg_copy: "overview.engine.ffmpegCopy",
  ffmpeg_transcode: "overview.engine.ffmpegTranscode",
};

const SESSION_STATES = {
  running: ["overview.session.running", "success"],
  starting: ["overview.session.starting", "warning"],
  restarting: ["overview.session.restarting", "warning"],
  failed: ["overview.session.failed", "danger"],
};

function engineLabel(session) {
  const name = ENGINE_LABELS[session.engine] ? i18n.t(ENGINE_LABELS[session.engine]) : session.engine || "—";
  if (!session.fallback_reason) return name;
  return i18n.t("overview.engine.fallback", { engine: name, reason: session.fallback_reason });
}

function sessionStateBadge(state) {
  const [key, tone] = SESSION_STATES[state] || ["overview.session.unknown", "neutral"];
  return badge(i18n.t(key, { state: state || "—" }), tone);
}

function showSessionError(channelID, message, returnFocusID) {
  openModal({
    title: i18n.t("overview.error.title"),
    description: i18n.t("overview.error.description", { channel: channelID }),
    body: h("code", { class: "code-block mono", text: message }),
    actions: [copyButton(message, { size: "" }), button(vt("common.close"), { onClick: closeModal })],
    onClose: () => document.getElementById(returnFocusID)?.focus(),
  });
}

function errorCell(session) {
  const message = (session.last_error || "").trim();
  if (!message) return h("td", { class: "muted", text: "—" });
  const controlID = `session-error-${encodeURIComponent(session.channel_id)}`;
  const control = button(errorSummary(message), {
    kind: "danger",
    size: "small",
    iconName: "circle-alert",
    ariaLabel: i18n.t("overview.error.view", { channel: session.channel_id }),
    onClick: () => showSessionError(session.channel_id, message, controlID),
  });
  control.id = controlID;
  return h("td", {}, control);
}

export async function renderOverview(ctx) {
  const [, , epgSourceData, epgMatchData] = await Promise.all([
    loadCatalog({ signal: ctx.signal }),
    refreshStatus(),
    endpoints.epgSources(ctx.signal),
    endpoints.epgMatches(ctx.signal),
  ]);
  if (!ctx.alive()) return frag();

  const enabledChannels = store.channels.filter((channel) => !channel.disabled).length;
  const enabledSourceIDs = new Set((epgSourceData.sources || []).filter((item) => item.enabled).map((item) => item.source.id));
  const programmeCount = (epgSourceData.statuses || [])
    .filter((status) => enabledSourceIDs.has(status.source_id))
    .reduce((total, status) => total + Number(status.programme_count || 0), 0);
  const matchedChannels = (epgMatchData.matches || []).filter((item) => item.status === "matched").length;
  const metrics = [
    { key: "channels", label: i18n.t("overview.metric.channels"), meta: i18n.t("overview.metric.channelsMeta"), read: () => formatNumber(store.channels.length) },
    { key: "enabled", label: i18n.t("overview.metric.enabled"), meta: i18n.t("overview.metric.enabledMeta"), read: () => formatNumber(enabledChannels) },
    { key: "active", label: i18n.t("overview.metric.active"), meta: i18n.t("overview.metric.activeMeta"), read: (status) => formatNumber(status?.session_count) },
    {
      key: "coverage",
      label: i18n.t("overview.metric.coverage"),
      meta: i18n.t("overview.metric.coverageMeta"),
      read: () => `${formatNumber(matchedChannels)} / ${formatNumber(enabledChannels)}`,
    },
    { key: "programmes", label: i18n.t("overview.metric.programmes"), meta: i18n.t("overview.metric.programmesMeta"), read: () => formatNumber(programmeCount) },
  ];
  const metricValues = new Map();
  const metricCards = metrics.map(({ key, label, meta, read }) => {
    const value = numberRoll();
    metricValues.set(key, { roll: value, read });
    return h(
      "article",
      { class: "metric" },
      h("span", { class: "metric-label", text: label }),
      h("strong", { class: "metric-value" }, value.node),
      h("span", { class: "metric-meta", text: meta }),
    );
  });

  const sessionBody = h("div", {});
  const sessionCount = h("p", { text: "" });
  const goroutinesRoll = numberRoll();
  const syncedRoll = numberRoll();
  const goroutines = h("strong", { class: "mono" }, goroutinesRoll.node);
  const syncedAt = h("strong", { class: "mono" }, syncedRoll.node);
  const httpDetail = h("small", { text: i18n.t("overview.health.ok") });
  const httpBadge = h("span", { class: "state-slot" });
  const healthTone = h(
    "div",
    { class: "health-row" },
    h("div", { class: "health-item" }, h("span", {}, h("strong", { text: i18n.t("overview.health.http") }), httpDetail), httpBadge),
    h("div", { class: "health-item" }, h("span", {}, h("strong", { text: i18n.t("overview.health.goroutines") }), h("small", { text: i18n.t("overview.health.runtime") })), goroutines),
    h("div", { class: "health-item" }, h("span", {}, h("strong", { text: i18n.t("overview.health.lastSync") }), h("small", { text: i18n.t("overview.health.autoRefresh") })), syncedAt),
  );
  let lastOnline = null;

  const stop = async (channelID) => {
    const accepted = await confirmDialog({
      title: i18n.t("overview.stop.title"),
      description: i18n.t("overview.stop.description", { channel: channelID }),
      warning: i18n.t("overview.stop.warning"),
      confirmLabel: i18n.t("overview.stop.confirm"),
    });
    if (!accepted) return;
    try {
      await endpoints.stopSession(channelID);
      toast(i18n.t("overview.stop.done"));
      await refreshStatus();
    } catch (error) {
      toastError(error, i18n.t("overview.stop.failed"));
    }
  };

  const paint = () => {
    if (!ctx.alive()) return;
    const status = store.status;
    for (const { roll, read } of metricValues.values()) roll.set(read(status));
    goroutinesRoll.set(formatNumber(status?.goroutines));
    syncedRoll.set(store.lastSyncAt ? formatClock(new Date(store.lastSyncAt)) : "—");

    if (store.online !== lastOnline) {
      lastOnline = store.online;
      httpDetail.textContent = i18n.t(store.online ? "overview.health.ok" : "overview.health.unreachable");
      httpBadge.replaceChildren(store.online ? badge(i18n.t("overview.health.normal"), "success", "circle-check") : badge(i18n.t("overview.health.abnormal"), "danger", "circle-alert"));
    }

    const sessions = status?.sessions || [];
    sessionCount.textContent = i18n.t(sessions.length ? "overview.session.count" : "overview.session.none", { count: sessions.length });

    if (!sessions.length) {
      sessionBody.replaceChildren(
        emptyState(i18n.t("overview.session.emptyTitle"), i18n.t("overview.session.emptyDescription"), linkButton(i18n.t("overview.session.viewChannels"), "/admin/channels", { iconName: "tv" })),
      );
      return;
    }

    const rows = sessions.map((session) =>
      h(
        "tr",
        {},
        h("td", {}, h("a", { class: "cell-link mono truncate", href: `/admin/channels/${encodeURIComponent(session.channel_id)}`, "data-route": true, text: session.channel_id })),
        h("td", {}, sessionStateBadge(session.state)),
        h("td", { text: (session.mode || "—").toUpperCase() }),
        h("td", { class: "muted truncate", text: engineLabel(session) }),
        h("td", { class: "mono muted truncate", text: session.pack_mode || "—" }),
        errorCell(session),
        h("td", {}, h("div", { class: "row-actions" }, button(i18n.t("overview.stop.action"), { kind: "danger", size: "small", onClick: () => stop(session.channel_id) }))),
      ),
    );
    const offset = sessionBody.querySelector(".table-wrap")?.scrollLeft || 0;
    sessionBody.replaceChildren(table([
      i18n.t("overview.table.channel"),
      i18n.t("overview.table.status"),
      i18n.t("overview.table.type"),
      i18n.t("overview.table.engine"),
      i18n.t("overview.table.packMode"),
      i18n.t("overview.table.lastError"),
      "",
    ], rows));
    if (offset) sessionBody.querySelector(".table-wrap").scrollLeft = offset;
  };

  paint();
  ctx.watchStatus(paint);

  return frag(
    pageHead(i18n.t("overview.title"), i18n.t("overview.description"), [
      button(i18n.t("overview.refresh"), { iconName: "refresh-cw", onClick: () => ctx.reload() }),
    ]),
    h(
      "div",
      { class: "stack" },
      h("div", { class: "metrics" }, metricCards),
      h(
        "div",
        { class: "split" },
        card({ title: i18n.t("overview.sessionsTitle"), description: "", body: sessionBody, flush: true, action: sessionCount }),
        card({ title: i18n.t("overview.healthTitle"), body: healthTone }),
      ),
    ),
  );
}
