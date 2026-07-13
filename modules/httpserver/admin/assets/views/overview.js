import { formatBytes, formatClock, formatDuration, formatNumber, frag, h } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { loadCatalog, refreshStatus, store } from "/admin/assets/core/store.js";
import { badge, button, card, emptyState, linkButton, pageHead, sessionStateBadge, table } from "/admin/assets/ui/kit.js";
import { confirmDialog, toast, toastError } from "/admin/assets/ui/overlay.js";

const ENGINE_LABELS = {
  native_rewrite: "原生",
  native_remux: "原生重封装",
  ffmpeg_copy: "ffmpeg 转封装",
  ffmpeg_transcode: "ffmpeg 转码",
};

// A channel that fell back to ffmpeg should say so, and say why: an engine
// label alone hides the fact that the native path declined the source.
function engineLabel(session) {
  const name = ENGINE_LABELS[session.engine] || session.engine || "—";
  if (!session.fallback_reason) return name;
  return `${name}（回退：${session.fallback_reason}）`;
}

const METRICS = [
  { key: "uptime_sec", label: "运行时间", meta: "当前进程", format: formatDuration },
  { key: "requests", label: "累计请求", meta: "HTTP 请求总数", format: formatNumber },
  { key: "errors", label: "累计错误", meta: "自启动以来", format: formatNumber },
  { key: "bytes_in", label: "接收流量", meta: "来自节目源", format: formatBytes },
  { key: "bytes_out", label: "发送流量", meta: "发送至播放器", format: formatBytes },
];

export async function renderOverview(ctx) {
  await Promise.all([loadCatalog({ signal: ctx.signal }), refreshStatus()]);
  if (!ctx.alive()) return frag();

  const metricValues = new Map();
  const metricCards = METRICS.map(({ key, label, meta, format }) => {
    const value = h("strong", { class: "metric-value", text: format(store.status?.[key]) });
    metricValues.set(key, { node: value, format });
    return h(
      "article",
      { class: "metric" },
      h("span", { class: "metric-label", text: label }),
      value,
      h("span", { class: "metric-meta", text: meta }),
    );
  });

  const sessionBody = h("div", {});
  const sessionCount = h("p", { text: "" });
  const goroutines = h("strong", { class: "mono", text: "—" });
  const syncedAt = h("strong", { class: "mono", text: "—" });
  const httpDetail = h("small", { text: "健康检查通过" });
  const httpBadge = h("span", { class: "state-slot" });
  const healthTone = h(
    "div",
    { class: "health-row" },
    h("div", { class: "health-item" }, h("span", {}, h("strong", { text: "HTTP 服务" }), httpDetail), httpBadge),
    h("div", { class: "health-item" }, h("span", {}, h("strong", { text: "并发协程" }), h("small", { text: "Go runtime" })), goroutines),
    h("div", { class: "health-item" }, h("span", {}, h("strong", { text: "最近同步" }), h("small", { text: "每秒自动刷新" })), syncedAt),
  );
  let lastOnline = null;

  const stop = async (channelID) => {
    const accepted = await confirmDialog({
      title: "停止会话？",
      description: `频道 ${channelID} 的打包进程会立即停止，下次播放需要重新冷启动。`,
      warning: "正在观看的播放器会立即中断。",
      confirmLabel: "停止会话",
    });
    if (!accepted) return;
    try {
      await endpoints.stopSession(channelID);
      toast("会话已停止");
      await refreshStatus();
    } catch (error) {
      toastError(error, "停止失败");
    }
  };

  const paint = () => {
    if (!ctx.alive()) return;
    const status = store.status;
    for (const [key, { node, format }] of metricValues) node.textContent = format(status?.[key]);
    goroutines.textContent = formatNumber(status?.goroutines);
    syncedAt.textContent = store.lastSyncAt ? formatClock(new Date(store.lastSyncAt)) : "—";

    if (store.online !== lastOnline) {
      lastOnline = store.online;
      httpDetail.textContent = store.online ? "健康检查通过" : "无法连接到服务";
      httpBadge.replaceChildren(store.online ? badge("正常", "success", "circle-check") : badge("异常", "danger", "circle-alert"));
    }

    const sessions = status?.sessions || [];
    sessionCount.textContent = sessions.length ? `${sessions.length} 个会话正在运行` : "没有正在运行的会话";

    if (!sessions.length) {
      sessionBody.replaceChildren(
        emptyState("当前没有播放会话", "频道开始播放后，运行状态会实时显示在这里。", linkButton("查看所有频道", "/admin/channels", { iconName: "tv" })),
      );
      return;
    }

    const rows = sessions.map((session) =>
      h(
        "tr",
        {},
        h("td", {}, h("a", { class: "cell-link mono", href: `/admin/channels/${encodeURIComponent(session.channel_id)}`, "data-route": true, text: session.channel_id })),
        h("td", {}, sessionStateBadge(session.state)),
        h("td", { text: (session.mode || "—").toUpperCase() }),
        h("td", { class: "mono muted", text: engineLabel(session) }),
        h("td", { class: "mono muted", text: session.pack_mode || "—" }),
        h("td", { class: "muted truncate", text: session.last_error || "—" }),
        h("td", {}, h("div", { class: "row-actions" }, button("停止", { kind: "danger", size: "small", onClick: () => stop(session.channel_id) }))),
      ),
    );
    sessionBody.replaceChildren(table(["频道", "状态", "类型", "引擎", "打包模式", "最近错误", ""], rows));
  };

  paint();
  ctx.watchStatus(paint);

  return frag(
    pageHead("总览", "实时查看服务运行情况、累计流量与当前播放会话。", [
      button("立即刷新", { iconName: "refresh-cw", onClick: () => refreshStatus() }),
    ]),
    h(
      "div",
      { class: "stack" },
      h("div", { class: "metrics" }, metricCards),
      h(
        "div",
        { class: "split" },
        card({ title: "活动会话", description: "", body: sessionBody, flush: true, action: sessionCount }),
        card({ title: "实例健康", body: healthTone }),
      ),
    ),
  );
}
