import { frag, h, icon } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { invalidateCatalog, loadCatalog, refreshStatus, sessionFor, sourceURL, store } from "/admin/assets/core/store.js";
import { badge, button, card, channelCell, emptyState, field, iconButton, input, linkButton, notice, pageHead, runModeLabel, select, stateBadge } from "/admin/assets/ui/kit.js";
import { closeModal, confirmDialog, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";
import { matchBadge, matchMap } from "/admin/assets/views/epg.js";
import { previewChannel } from "/admin/assets/views/preview.js";

export function matchesQuery(channel, query) {
  if (!query) return true;
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  return `${channel.title} ${channel.id} ${channel.group || ""}`.toLowerCase().includes(needle);
}

export async function renderChannels(ctx) {
  const [, matchData] = await Promise.all([
    loadCatalog({ force: true, signal: ctx.signal }),
    endpoints.epgMatches(ctx.signal),
    refreshStatus(),
  ]);
  if (!ctx.alive()) return frag();

  const matches = matchMap(matchData.matches);
  const epgCell = (channel) =>
    channel.disabled ? h("span", { class: "muted", text: "—" }) : matchBadge(matches.get(channel.id)?.status);
  let query = "";
  const filter = input("filter", "", { placeholder: "筛选名称、标识符或分组…", type: "search" });
  filter.setAttribute("aria-label", "筛选频道");

  const count = h("span", { class: "muted", text: "" });
  const tbody = h("tbody");
  const cards = h("div", { class: "record-list" });
  const mobileMedia = matchMedia("(max-width: 680px)");

  const move = async (id, delta) => {
    const ids = store.channels.map((channel) => channel.id);
    const from = ids.indexOf(id);
    const to = from + delta;
    if (from < 0 || to < 0 || to >= ids.length) return;
    [ids[from], ids[to]] = [ids[to], ids[from]];
    try {
      const revisions = Object.fromEntries(store.channels.map((channel) => [channel.id, channel.revision]));
      await endpoints.reorderChannels(ids, revisions);
      invalidateCatalog();
      toast("频道顺序已更新");
      await ctx.reload();
    } catch (error) {
      toastError(error, "排序失败");
    }
  };

  const stateOf = (channel) => {
    if (channel.disabled) return "disabled";
    const session = sessionFor(channel.id);
    return session ? session.state || "unknown" : "idle";
  };

  // Rows build once per layout; filtering and polling only touch what changed.
  const entries = store.channels.map((channel, index) => ({
    channel,
    index,
    state: "",
    tr: null,
    slot: null,
    up: null,
    down: null,
    card: null,
    cardSlot: null,
  }));

  const buildRow = (entry) => {
    const { channel } = entry;
    const route = `/admin/channels/${encodeURIComponent(channel.id)}`;
    entry.state = stateOf(channel);
    entry.slot = h("span", { class: "state-slot" }, stateBadge(channel, sessionFor(channel.id)));
    entry.up = iconButton("arrow-up", `上移 ${channel.title}`, { variant: "outline", onClick: () => move(channel.id, -1) });
    entry.down = iconButton("arrow-down", `下移 ${channel.title}`, { variant: "outline", onClick: () => move(channel.id, 1) });
    entry.tr = h(
      "tr",
      {},
      h("td", {}, h("a", { class: "cell-link", href: route, "data-route": true }, channelCell(channel))),
      h(
        "td",
        {},
        h(
          "div",
          { class: "source-cell" },
          h("span", { class: "mono truncate", text: sourceURL(channel.upstream, channel.path) }),
          h("small", { text: channel.upstream ? `来源服务器：${channel.upstream}` : "未设置来源服务器" }),
        ),
      ),
      h("td", {}, badge((channel.ingress || "—").toUpperCase(), "neutral")),
      h("td", { class: "muted", text: runModeLabel(channel) }),
      h("td", {}, epgCell(channel)),
      h("td", {}, entry.slot),
      h(
        "td",
        {},
        h(
          "div",
          { class: "row-actions" },
          iconButton("play", `预览 ${channel.title}`, { variant: "outline", disabled: channel.disabled, onClick: () => previewChannel(channel) }),
          entry.up,
          entry.down,
          linkButton("配置", route, { iconName: "sliders-horizontal" }),
        ),
      ),
    );
  };

  const buildCard = (entry) => {
    const { channel } = entry;
    const route = `/admin/channels/${encodeURIComponent(channel.id)}`;
    entry.state = stateOf(channel);
    entry.cardSlot = h("span", { class: "state-slot" }, stateBadge(channel, sessionFor(channel.id)));
    entry.card = h(
      "article",
      { class: "record" },
      h("div", { class: "record-head" }, channelCell(channel), entry.cardSlot),
      h("div", { class: "record-source mono truncate", text: sourceURL(channel.upstream, channel.path) }),
      h(
        "div",
        { class: "record-meta" },
        h("span", {}, h("small", { text: "格式" }), h("span", { text: (channel.ingress || "—").toUpperCase() })),
        h("span", {}, h("small", { text: "运行方式" }), h("span", { text: runModeLabel(channel) })),
        h("span", {}, h("small", { text: "节目单" }), epgCell(channel)),
      ),
      h(
        "div",
        { class: "record-actions" },
        linkButton("配置频道", route, { kind: "primary", iconName: "sliders-horizontal" }),
        button("预览", { iconName: "play", disabled: channel.disabled, onClick: () => previewChannel(channel) }),
      ),
    );
  };

  const emptyCell = h("td", { colspan: 7 });
  const emptyRow = h("tr", { hidden: true }, emptyCell);
  const emptyCard = h("div", { hidden: true });
  let emptyKind = "";

  const clearFilter = () => {
    filter.value = "";
    query = "";
    applyFilter();
  };

  const makeEmpty = () =>
    query
      ? emptyState("没有匹配的频道", "换一个名称、标识符或分组试试。", button("清除筛选", { onClick: clearFilter }))
      : emptyState("还没有频道", "添加第一个频道，或从 M3U 播放列表批量导入。", linkButton("添加频道", "/admin/channels/new", { kind: "primary", iconName: "plus" }));

  const buildTable = () => {
    for (const entry of entries) {
      buildRow(entry);
      tbody.append(entry.tr);
    }
    tbody.append(emptyRow);
  };

  const buildCards = () => {
    for (const entry of entries) {
      buildCard(entry);
      cards.append(entry.card);
    }
    cards.append(emptyCard);
  };

  const applyFilter = () => {
    if (mobileMedia.matches) {
      if (!cards.childElementCount) buildCards();
    } else if (!tbody.childElementCount) {
      buildTable();
    }

    const locked = Boolean(query.trim());
    let shown = 0;
    for (const entry of entries) {
      const visible = matchesQuery(entry.channel, query);
      if (visible) shown += 1;
      if (entry.tr) entry.tr.hidden = !visible;
      if (entry.card) entry.card.hidden = !visible;
      if (entry.up) entry.up.disabled = locked || entry.index === 0;
      if (entry.down) entry.down.disabled = locked || entry.index === entries.length - 1;
    }

    count.textContent = query ? `${shown} / ${entries.length} 个频道` : `${entries.length} 个频道`;

    const kind = query ? "filtered" : "blank";
    if (!shown && emptyKind !== kind) {
      emptyKind = kind;
      emptyCell.replaceChildren(makeEmpty());
      emptyCard.replaceChildren(makeEmpty());
    }
    emptyRow.hidden = Boolean(shown);
    emptyCard.hidden = Boolean(shown);
  };

  const repaintStates = () => {
    if (!ctx.alive()) return;
    for (const entry of entries) {
      const state = stateOf(entry.channel);
      if (state === entry.state) continue;
      entry.state = state;
      const session = sessionFor(entry.channel.id);
      entry.slot?.replaceChildren(stateBadge(entry.channel, session));
      entry.cardSlot?.replaceChildren(stateBadge(entry.channel, session));
    }
  };

  filter.addEventListener("input", () => {
    query = filter.value;
    applyFilter();
  });

  const onLayoutChange = () => applyFilter();
  mobileMedia.addEventListener("change", onLayoutChange);
  ctx.onDispose(() => mobileMedia.removeEventListener("change", onLayoutChange));

  applyFilter();
  ctx.watchStatus(repaintStates);

  return frag(
    pageHead("频道", "管理节目源、运行方式与播放状态。", [
      button("全部停用", { iconName: "ban", disabled: !store.channels.length, onClick: () => setAll(ctx, true) }),
      button("全部启用", { iconName: "power", disabled: !store.channels.length, onClick: () => setAll(ctx, false) }),
      button("导入 M3U", { iconName: "upload", onClick: () => openImportModal(ctx) }),
      linkButton("添加频道", "/admin/channels/new", { kind: "primary", iconName: "plus" }),
    ]),
    h("div", { class: "toolbar" }, h("div", { class: "search-field" }, icon("search", 18), filter), count),
    card({
      body: frag(h("div", { class: "desktop-only" }, h("div", { class: "table-wrap" }, h("table", {}, h("thead", {}, h("tr", {}, ["频道", "节目源", "格式", "运行方式", "节目单", "状态", ""].map((label) => h("th", { text: label })))), tbody))), h("div", { class: "mobile-only" }, cards)),
      flush: true,
    }),
  );
}

async function setAll(ctx, disabled) {
  const accepted = await confirmDialog({
    title: disabled ? "停用全部频道？" : "启用全部频道？",
    description: disabled
      ? "所有频道都会从目录中隐藏，播放地址随即失效。"
      : "所有频道都会重新出现在目录中，并接受播放请求。",
    warning: disabled ? "正在进行的会话会立即中断。" : "",
    confirmLabel: disabled ? "全部停用" : "全部启用",
    tone: disabled ? "danger" : "primary",
  });
  if (!accepted) return;
  try {
    const result = disabled ? await endpoints.disableAllChannels() : await endpoints.enableAllChannels();
    invalidateCatalog();
    toast(disabled ? "全部频道已停用" : "全部频道已启用", `${result.changed || 0} 个频道发生变更。`);
    await ctx.reload();
  } catch (error) {
    toastError(error, disabled ? "停用失败" : "启用失败");
  }
}

function openImportModal(ctx) {
  const upstream = select("default_upstream", [["", "选择默认来源服务器"], ...store.upstreams.map((item) => [item.id, `${item.id} · ${item.base_url}`])], "");
  const content = h("textarea", { name: "content", placeholder: "#EXTM3U\n#EXTINF:-1,频道名称\nhttps://…", rows: 8 });
  const result = h("div", {});
  let parsed = [];

  const applyButton = button("确认导入", {
    kind: "primary",
    disabled: true,
    onClick: async () => {
      try {
        const revisions = Object.fromEntries(store.channels.map((channel) => [channel.id, channel.revision]));
        const data = await endpoints.importM3U({
          entries: parsed,
          default_upstream: upstream.value,
          revisions,
          apply: true,
        });
        closeModal();
        invalidateCatalog();
        toast("导入完成", `写入 ${data.created} 项，跳过 ${data.skipped} 项`);
        await ctx.reload();
      } catch (error) {
        toastError(error, "导入失败");
      }
    },
  });

  const previewButton = button("解析预览", {
    onClick: async () => {
      try {
        const data = await endpoints.importM3U({ content: content.value, default_upstream: upstream.value, apply: false });
        parsed = data.entries || [];
        const existing = new Set(store.channels.map((channel) => channel.id));
        const groups = { create: [], update: [], skip: [] };
        for (const entry of parsed) {
          if (entry.skip || !entry.suggested_id) groups.skip.push(entry);
          else if (existing.has(entry.suggested_id)) groups.update.push(entry);
          else groups.create.push(entry);
        }
        result.replaceChildren(
          h(
            "div",
            { class: "stack" },
            notice(`已解析 ${data.count} 项。导入不会删除列表中缺失的频道。`),
            h(
              "div",
              { class: "badge-row" },
              badge(`新增 ${groups.create.length}`, "success"),
              badge(`更新 ${groups.update.length}`, "warning"),
              badge(`跳过 ${groups.skip.length}`, groups.skip.length ? "danger" : "neutral"),
            ),
            h(
              "div",
              { class: "list" },
              [...groups.create, ...groups.update, ...groups.skip].slice(0, 40).map((entry) =>
                h(
                  "div",
                  { class: "list-item" },
                  h("span", {}, h("strong", { text: entry.title || entry.suggested_id || "无法识别" }), h("small", { class: "mono", text: entry.suggested_path || entry.note || "—" })),
                  entry.skip
                    ? badge("跳过", "danger")
                    : existing.has(entry.suggested_id)
                      ? badge("更新", "warning")
                      : badge("新增", "success"),
                ),
              ),
            ),
          ),
        );
        applyButton.disabled = !parsed.length;
      } catch (error) {
        toastError(error, "解析失败");
      }
    },
  });

  openModal({
    title: "批量导入 M3U",
    description: "先解析预览，确认映射结果后再写入。",
    body: h("div", { class: "stack" }, field("默认来源服务器", upstream), field("M3U 内容", content), result),
    actions: [button("取消", { onClick: closeModal }), previewButton, applyButton],
  });
}
