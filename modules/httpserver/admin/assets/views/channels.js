import { frag, h, icon } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { invalidateCatalog, loadCatalog, refreshStatus, sessionFor, sourceURL, store } from "/admin/assets/core/store.js";
import { badge, button, card, channelCell, emptyState, field, iconButton, input, linkButton, notice, pageHead, runModeLabel, select, stateBadge } from "/admin/assets/ui/kit.js";
import { closeModal, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";
import { previewChannel } from "/admin/assets/views/preview.js";

export function matchesQuery(channel, query) {
  if (!query) return true;
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  return `${channel.title} ${channel.id} ${channel.group || ""}`.toLowerCase().includes(needle);
}

export async function renderChannels(ctx) {
  await loadCatalog({ force: true, signal: ctx.signal });
  await refreshStatus();
  if (!ctx.alive()) return frag();

  let query = "";
  const filter = input("filter", "", { placeholder: "筛选名称、标识符或分组…", type: "search" });
  filter.setAttribute("aria-label", "筛选频道");

  const count = h("span", { class: "muted", text: "" });
  const tbody = h("tbody");
  const cards = h("div", { class: "record-list" });
  // channel id -> badge slot, so polling swaps the badge in place instead of
  // rebuilding the table and blowing away the filter and scroll position.
  const stateSlots = new Map();

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

  const draw = () => {
    const visible = store.channels.filter((channel) => matchesQuery(channel, query));
    stateSlots.clear();
    tbody.replaceChildren();
    cards.replaceChildren();
    count.textContent = query
      ? `${visible.length} / ${store.channels.length} 个频道`
      : `${store.channels.length} 个频道`;

    if (!visible.length) {
      const empty = query
        ? emptyState("没有匹配的频道", "换一个名称、标识符或分组试试。", button("清除筛选", { onClick: () => { filter.value = ""; query = ""; draw(); } }))
        : emptyState("还没有频道", "添加第一个频道，或从 M3U 播放列表批量导入。", linkButton("添加频道", "/admin/channels/new", { kind: "primary", iconName: "plus" }));
      tbody.append(h("tr", {}, h("td", { colspan: 6 }, empty)));
      cards.append(empty.cloneNode(true));
      return;
    }

    for (const channel of visible) {
      const route = `/admin/channels/${encodeURIComponent(channel.id)}`;
      const position = store.channels.findIndex((item) => item.id === channel.id);
      const reorderLocked = Boolean(query);

      const slot = h("span", { class: "state-slot" }, stateBadge(channel, sessionFor(channel.id)));
      stateSlots.set(channel.id, slot);

      tbody.append(
        h(
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
          h("td", {}, slot),
          h(
            "td",
            {},
            h(
              "div",
              { class: "row-actions" },
              iconButton("play", `预览 ${channel.title}`, { variant: "outline", disabled: channel.disabled, onClick: () => previewChannel(channel) }),
              iconButton("arrow-up", `上移 ${channel.title}`, { variant: "outline", disabled: reorderLocked || position <= 0, onClick: () => move(channel.id, -1) }),
              iconButton("arrow-down", `下移 ${channel.title}`, { variant: "outline", disabled: reorderLocked || position >= store.channels.length - 1, onClick: () => move(channel.id, 1) }),
              linkButton("配置", route, { iconName: "sliders-horizontal" }),
            ),
          ),
        ),
      );

      const cardSlot = h("span", { class: "state-slot" }, stateBadge(channel, sessionFor(channel.id)));
      cards.append(
        h(
          "article",
          { class: "record" },
          h("div", { class: "record-head" }, channelCell(channel), cardSlot),
          h("div", { class: "record-source mono truncate", text: sourceURL(channel.upstream, channel.path) }),
          h(
            "div",
            { class: "record-meta" },
            h("span", {}, h("small", { text: "格式" }), h("span", { text: (channel.ingress || "—").toUpperCase() })),
            h("span", {}, h("small", { text: "运行方式" }), h("span", { text: runModeLabel(channel) })),
          ),
          h(
            "div",
            { class: "record-actions" },
            linkButton("配置频道", route, { kind: "primary", iconName: "sliders-horizontal" }),
            button("预览", { iconName: "play", disabled: channel.disabled, onClick: () => previewChannel(channel) }),
          ),
        ),
      );
      stateSlots.set(`card:${channel.id}`, cardSlot);
    }
  };

  const repaintStates = () => {
    if (!ctx.alive()) return;
    for (const channel of store.channels) {
      const next = () => stateBadge(channel, sessionFor(channel.id));
      stateSlots.get(channel.id)?.replaceChildren(next());
      stateSlots.get(`card:${channel.id}`)?.replaceChildren(next());
    }
  };

  filter.addEventListener("input", () => {
    query = filter.value;
    draw();
  });

  draw();
  ctx.watchStatus(repaintStates);

  return frag(
    pageHead("频道", "管理节目源、运行方式与播放状态。", [
      button("导入 M3U", { iconName: "upload", onClick: () => openImportModal(ctx) }),
      linkButton("添加频道", "/admin/channels/new", { kind: "primary", iconName: "plus" }),
    ]),
    h("div", { class: "toolbar" }, h("div", { class: "search-field" }, icon("search", 18), filter), count),
    card({
      body: frag(h("div", { class: "desktop-only" }, h("div", { class: "table-wrap" }, h("table", {}, h("thead", {}, h("tr", {}, ["频道", "节目源", "格式", "运行方式", "状态", ""].map((label) => h("th", { text: label })))), tbody))), h("div", { class: "mobile-only" }, cards)),
      flush: true,
    }),
  );
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
