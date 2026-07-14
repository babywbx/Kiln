import { frag, h, icon } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { invalidateCatalog, loadCatalog, refreshStatus, sessionFor, sourceURL, store } from "/admin/assets/core/store.js";
import { vt } from "/admin/assets/core/view-i18n.js";
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
  const filter = input("filter", "", { placeholder: vt("channels.filter"), type: "search" });
  filter.setAttribute("aria-label", vt("channels.filterAria"));

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
      toast(vt("channels.reordered"));
      await ctx.reload();
    } catch (error) {
      toastError(error, vt("channels.reorderFailed"));
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
    entry.up = iconButton("arrow-up", vt("channels.moveUp", { name: channel.title }), { variant: "outline", onClick: () => move(channel.id, -1) });
    entry.down = iconButton("arrow-down", vt("channels.moveDown", { name: channel.title }), { variant: "outline", onClick: () => move(channel.id, 1) });
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
          h("span", { class: "mono truncate", text: sourceURL(channel.upstream, channel.path, channel.source_url) }),
          h("small", { text: channel.source_url ? vt("channels.fullSource") : channel.upstream ? vt("channels.upstream", { id: channel.upstream }) : vt("channels.noSource") }),
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
          iconButton("play", vt("channels.previewNamed", { name: channel.title }), { variant: "outline", disabled: channel.disabled, onClick: () => previewChannel(channel) }),
          entry.up,
          entry.down,
          linkButton(vt("channels.configure"), route, { iconName: "sliders-horizontal" }),
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
      h("div", { class: "record-source mono truncate", text: sourceURL(channel.upstream, channel.path, channel.source_url) }),
      h(
        "div",
        { class: "record-meta" },
        h("span", {}, h("small", { text: vt("channels.format") }), h("span", { text: (channel.ingress || "—").toUpperCase() })),
        h("span", {}, h("small", { text: vt("channels.runMode") }), h("span", { text: runModeLabel(channel) })),
        h("span", {}, h("small", { text: vt("channels.epg") }), epgCell(channel)),
      ),
      h(
        "div",
        { class: "record-actions" },
        linkButton(vt("channels.configureChannel"), route, { kind: "primary", iconName: "sliders-horizontal" }),
        button(vt("channels.preview"), { iconName: "play", disabled: channel.disabled, onClick: () => previewChannel(channel) }),
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
      ? emptyState(vt("channels.noMatch"), vt("channels.noMatchHint"), button(vt("channels.clearFilter"), { onClick: clearFilter }))
      : emptyState(vt("channels.empty"), vt("channels.emptyHint"), linkButton(vt("channels.add"), "/admin/channels/new", { kind: "primary", iconName: "plus" }));

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

    count.textContent = query ? vt("channels.countFiltered", { shown, total: entries.length }) : vt("common.channels", { count: entries.length });

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
    pageHead(vt("channels.title"), vt("channels.description"), [
      button(vt("channels.disableAll"), { iconName: "ban", disabled: !store.channels.length, onClick: () => setAll(ctx, true) }),
      button(vt("channels.enableAll"), { iconName: "power", disabled: !store.channels.length, onClick: () => setAll(ctx, false) }),
      button(vt("channels.importM3U"), { iconName: "upload", onClick: () => openImportModal(ctx) }),
      linkButton(vt("channels.add"), "/admin/channels/new", { kind: "primary", iconName: "plus" }),
    ]),
    h("div", { class: "toolbar" }, h("div", { class: "search-field" }, icon("search", 18), filter), count),
    card({
      body: frag(h("div", { class: "desktop-only" }, h("div", { class: "table-wrap" }, h("table", {}, h("thead", {}, h("tr", {}, ["channel", "source", "format", "runMode", "epg", "status", ""].map((key) => h("th", { text: key ? vt(`channels.header.${key}`) : "" })))), tbody))), h("div", { class: "mobile-only" }, cards)),
      flush: true,
    }),
  );
}

async function setAll(ctx, disabled) {
  const accepted = await confirmDialog({
    title: vt(disabled ? "channels.disableAllTitle" : "channels.enableAllTitle"),
    description: disabled
      ? vt("channels.disableAllDesc")
      : vt("channels.enableAllDesc"),
    warning: disabled ? vt("channels.disableAllWarning") : "",
    confirmLabel: vt(disabled ? "channels.disableAll" : "channels.enableAll"),
    tone: disabled ? "danger" : "primary",
  });
  if (!accepted) return;
  try {
    const result = disabled ? await endpoints.disableAllChannels() : await endpoints.enableAllChannels();
    invalidateCatalog();
    toast(vt(disabled ? "channels.disabledAll" : "channels.enabledAll"), vt("channels.changed", { count: result.changed || 0 }));
    await ctx.reload();
  } catch (error) {
    toastError(error, vt(disabled ? "channels.disableFailed" : "channels.enableFailed"));
  }
}

function openImportModal(ctx) {
  const upstream = select("default_upstream", [["", vt("import.chooseUpstream")], ...store.upstreams.map((item) => [item.id, `${item.id} · ${item.base_url}`])], "");
  const content = h("textarea", { name: "content", placeholder: vt("import.contentPlaceholder"), rows: 8 });
  const result = h("div", {});
  let parsed = [];

  const applyButton = button(vt("import.apply"), {
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
        toast(vt("import.done"), vt("import.doneDetail", { created: data.created, skipped: data.skipped }));
        await ctx.reload();
      } catch (error) {
        toastError(error, vt("import.failed"));
      }
    },
  });

  const previewButton = button(vt("import.preview"), {
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
            notice(vt("import.parsed", { count: data.count })),
            h(
              "div",
              { class: "badge-row" },
              badge(vt("import.created", { count: groups.create.length }), "success"),
              badge(vt("import.updated", { count: groups.update.length }), "warning"),
              badge(vt("import.skipped", { count: groups.skip.length }), groups.skip.length ? "danger" : "neutral"),
            ),
            h(
              "div",
              { class: "list" },
              [...groups.create, ...groups.update, ...groups.skip].slice(0, 40).map((entry) =>
                h(
                  "div",
                  { class: "list-item" },
                  h("span", {}, h("strong", { text: entry.title || entry.suggested_id || vt("import.unrecognized") }), h("small", { class: "mono", text: entry.suggested_path || entry.note || "—" })),
                  entry.skip
                    ? badge(vt("import.skip"), "danger")
                    : existing.has(entry.suggested_id)
                      ? badge(vt("import.update"), "warning")
                      : badge(vt("import.create"), "success"),
                ),
              ),
            ),
          ),
        );
        applyButton.disabled = !parsed.length;
      } catch (error) {
        toastError(error, vt("import.parseFailed"));
      }
    },
  });

  openModal({
    title: vt("import.title"),
    description: vt("import.description"),
    body: h("div", { class: "stack" }, field(vt("import.defaultUpstream"), upstream), field(vt("import.content"), content), result),
    actions: [button(vt("common.cancel"), { onClick: closeModal }), previewButton, applyButton],
  });
}
