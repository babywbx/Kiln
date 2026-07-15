import { formatBytes, formatISOTime, formatNumber, frag, h } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { i18n } from "/admin/assets/core/i18n.js";
import { badge, button, card, emptyState, field, iconButton, input, notice, pageHead, select, table } from "/admin/assets/ui/kit.js";
import { closeModal, confirmDialog, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";

const ID_KINDS = {
  numeric: "epg.idKind.numeric",
  name: "epg.idKind.name",
  mixed: "epg.idKind.mixed",
};

const MATCH_TONES = {
  matched: ["epg.match.matched", "success"],
  suggested: ["epg.match.suggested", "warning"],
  unmatched: ["epg.match.unmatched", "danger"],
};

const PRESET_COPY = {
  "hk-1": ["epg.preset.hongKong", "epg.preset.hongKongDescription"],
  "tw-1": ["epg.preset.taiwan", "epg.preset.taiwanDescription"],
  "cn-1": ["epg.preset.china", "epg.preset.chinaDescription"],
  "global-1": ["epg.preset.global", "epg.preset.globalDescription"],
  "cn-2": ["epg.preset.chinaAlt1", "epg.preset.chinaAlt1Description"],
  "cn-3": ["epg.preset.chinaAlt2", "epg.preset.chinaAlt2Description"],
  fanmingming: ["epg.preset.chinaAlt3", "epg.preset.chinaAlt3Description"],
};

function sourceCopy(source) {
  const [nameKey, descriptionKey] = PRESET_COPY[source.id] || [];
  return {
    name: nameKey ? i18n.t(nameKey) : source.name || source.id,
    description: descriptionKey ? i18n.t(descriptionKey) : source.region === "custom" ? i18n.t("epg.customSourceDescription") : source.description,
  };
}

export function matchBadge(status) {
  if (!status) return badge(i18n.t("epg.match.disabled"), "neutral");
  const [key, tone] = MATCH_TONES[status] || ["epg.unknown", "neutral"];
  return badge(i18n.t(key), tone);
}

export function matchMap(matches) {
  return new Map((matches || []).map((item) => [item.channel_id, item]));
}

export async function renderEPG(ctx) {
  const [presetData, sourceData, matchData, egress] = await Promise.all([
    endpoints.epgPresets(ctx.signal),
    endpoints.epgSources(ctx.signal),
    endpoints.epgMatches(ctx.signal),
    endpoints.egress(ctx.signal),
  ]);
  if (!ctx.alive()) return frag();

  const presets = new Map((presetData.presets || []).map((preset) => [preset.id, preset]));
  const sources = sourceData.sources || [];
  const statuses = new Map((sourceData.statuses || []).map((status) => [status.source_id, status]));
  const proxies = egress.proxies || [];
  const matches = matchData.matches || [];
  const loadedProgrammeCount = [...statuses.values()].reduce((total, status) => total + Number(status.programme_count || 0), 0);

  const enabledCount = sources.filter((item) => item.enabled).length;
  const waitingForFirstRefresh =
    enabledCount > 0 && sources.filter((item) => item.enabled).every((item) => !statuses.get(item.source.id)?.last_attempt);

  const refreshButton = button(i18n.t("epg.refresh"), {
    kind: "primary",
    iconName: "refresh-cw",
    disabled: enabledCount === 0,
    onClick: async () => {
      refreshButton.disabled = true;
      toast(i18n.t("epg.refreshing"), i18n.t("epg.refreshingDescription"), "info");
      try {
        const result = await endpoints.refreshEPG();
        const failed = (result.statuses || []).filter((status) => status.error);
        const programmeCount = (result.statuses || []).reduce((total, status) => total + Number(status.programme_count || 0), 0);
        const summary = i18n.t("epg.refreshSummary", { programmes: formatNumber(programmeCount) });
        if (result.ok && !failed.length) toast(i18n.t("epg.refreshed"), summary);
        else toast(i18n.t("epg.refreshPartial"), i18n.t("epg.refreshPartialDescription", { failed: failed.length, summary }), "warning");
        await ctx.reload();
      } catch (error) {
        toastError(error, i18n.t("epg.refreshFailed"));
        refreshButton.disabled = false;
      }
    },
  });

  const reload = async () => {
    if (ctx.alive()) await ctx.reload();
  };

  const sourceBody = sources.length
    ? table(
        [i18n.t("epg.table.enabled"), i18n.t("epg.table.source"), i18n.t("epg.table.address"), i18n.t("epg.table.identity"), i18n.t("epg.table.status"), ""],
        sources.map((configured) => sourceRow(configured, presets, statuses, proxies, reload)),
      )
    : emptyState(i18n.t("epg.emptySources"), i18n.t("epg.emptySourcesDescription"));

  return frag(
    pageHead(i18n.t("epg.title"), i18n.t("epg.description"), [refreshButton]),
    h(
      "div",
      { class: "stack" },
      enabledCount === 0
        ? notice(i18n.t("epg.enableSourceNotice"), "info", "info")
        : waitingForFirstRefresh
          ? notice(i18n.t("epg.firstRefreshNotice"), "info", "download")
        : null,
      card({
        title: i18n.t("epg.sourcesTitle"),
        description: i18n.t("epg.sourcesDescription"),
        body: sourceBody,
        flush: true,
        action: button(i18n.t("epg.addSource"), {
          size: "small",
          iconName: "plus",
          onClick: () => openSourceModal({ presets, proxies, existing: sources.map((item) => item.source.id), after: reload }),
        }),
      }),
      card({
        title: i18n.t("epg.matchTitle"),
        description: i18n.t("epg.matchDescription"),
        body: matchSummary(matches, loadedProgrammeCount > 0),
      }),
    ),
  );
}

function sourceRow(configured, presets, statuses, proxies, after) {
  const source = configured.source;
  const copy = sourceCopy(source);
  const preset = presets.get(source.id) || null;
  const status = statuses.get(source.id) || null;

  const toggle = h("input", {
    type: "checkbox",
    checked: configured.enabled,
    "aria-label": i18n.t("epg.enableSourceAria", { source: copy.name }),
  });
  toggle.addEventListener("change", async () => {
    toggle.disabled = true;
    try {
      await persistSource({ ...draftOf(configured, preset), enabled: toggle.checked }, configured.revision);
      toast(i18n.t(toggle.checked ? "epg.sourceEnabled" : "epg.sourceDisabled"), toggle.checked ? i18n.t("epg.refreshHint") : "");
      await after();
    } catch (error) {
      toggle.checked = !toggle.checked;
      toggle.disabled = false;
      toastError(error, i18n.t("epg.updateFailed"));
    }
  });

  const remove = async () => {
    const accepted = await confirmDialog({
      title: i18n.t(preset ? "epg.restoreTitle" : "epg.deleteTitle"),
      description: preset
        ? i18n.t("epg.restoreDescription", { source: copy.name })
        : i18n.t("epg.deleteDescription", { source: copy.name }),
      confirmLabel: i18n.t(preset ? "epg.restoreConfirm" : "epg.deleteConfirm"),
    });
    if (!accepted) return;
    try {
      await endpoints.deleteEPGSource(source.id, configured.revision);
      toast(i18n.t(preset ? "epg.restored" : "epg.deleted"));
      await after();
    } catch (error) {
      toastError(error, i18n.t("epg.deleteFailed"));
    }
  };

  return h(
    "tr",
    {},
    h("td", {}, h("label", { class: "check-row" }, toggle)),
    h(
      "td",
      {},
      h(
        "div",
        { class: "source-cell" },
        h("strong", { text: copy.name }),
        h("small", { class: "mono", text: source.id }),
        copy.description ? h("small", { text: copy.description }) : null,
      ),
    ),
    h(
      "td",
      {},
      h(
        "div",
        { class: "source-cell" },
        h("span", { class: "mono truncate", text: source.url || i18n.t("epg.addressMissing") }),
        h("small", { text: source.approx_bytes ? i18n.t("epg.approxSize", { size: formatBytes(source.approx_bytes) }) : i18n.t("epg.sizeUnknown") }),
      ),
    ),
    h(
      "td",
      {},
      h(
        "div",
        { class: "badge-row" },
        badge(source.region || "custom", "neutral"),
        badge(ID_KINDS[source.id_kind] ? i18n.t(ID_KINDS[source.id_kind]) : source.id_kind || i18n.t("epg.unknown"), "neutral"),
        badge(source.timezone || i18n.t("epg.defaultTimezone"), "neutral"),
        badge(i18n.t("epg.egressBadge", { proxy: source.proxy || "auto" }), "neutral"),
      ),
    ),
    h("td", {}, statusCell(configured.enabled, status)),
    h(
      "td",
      {},
      h(
        "div",
        { class: "row-actions" },
        iconButton("pencil", i18n.t("epg.editSourceAria", { source: copy.name }), {
          variant: "outline",
          onClick: () => openSourceModal({ presets, proxies, configured, after }),
        }),
        iconButton("trash-2", i18n.t(preset ? "epg.restoreSourceAria" : "epg.deleteSourceAria", { source: copy.name }), {
          kind: "danger",
          variant: "outline",
          disabled: !configured.revision,
          onClick: remove,
        }),
      ),
    ),
  );
}

function statusCell(enabled, status) {
  if (!enabled) return h("span", { class: "muted", text: i18n.t("shared.disabled") });
  if (!status || !status.last_attempt) {
    return h("div", { class: "source-cell" }, badge(i18n.t("epg.status.waiting"), "warning"), h("small", { text: i18n.t("epg.status.notDownloaded") }));
  }

  const badges = h("div", { class: "badge-row" });
  if (status.error) badges.append(badge(i18n.t("epg.status.fetchFailed"), "danger", "circle-alert"));
  else if (status.stale) badges.append(badge(i18n.t("epg.status.stale"), "warning"));
  else if (status.available) badges.append(badge(i18n.t("epg.status.normal"), "success", "circle-check"));
  else badges.append(badge(i18n.t("epg.status.noData"), "neutral"));

  return h(
    "div",
    { class: "source-cell" },
    badges,
    h("small", { text: i18n.t("epg.status.counts", { channels: formatNumber(status.channel_count), programmes: formatNumber(status.programme_count) }) }),
    h("small", { text: status.last_success ? i18n.t("epg.status.lastSuccess", { time: formatISOTime(status.last_success) }) : i18n.t("epg.status.neverSucceeded") }),
    status.error ? h("small", { class: "text-danger truncate", title: status.error, text: status.error }) : null,
  );
}

function matchSummary(matches, hasLoadedProgrammes) {
  if (!matches.length) {
    return emptyState(i18n.t("epg.match.empty"), i18n.t("epg.match.emptyDescription"));
  }
  const counts = { matched: 0, suggested: 0, unmatched: 0 };
  for (const item of matches) {
    if (item.status in counts) counts[item.status] += 1;
  }
  return h(
    "div",
    { class: "stack" },
    h(
      "div",
      { class: "badge-row" },
      badge(i18n.t("epg.match.matchedCount", { count: counts.matched }), counts.matched ? "success" : "neutral"),
      badge(i18n.t("epg.match.suggestedCount", { count: counts.suggested }), counts.suggested ? "warning" : "neutral"),
      badge(i18n.t("epg.match.unmatchedCount", { count: counts.unmatched }), counts.unmatched ? "danger" : "neutral"),
    ),
    hasLoadedProgrammes && counts.matched === 0
      ? notice(
          counts.suggested > 0
            ? i18n.t("epg.match.confirmNotice")
            : i18n.t("epg.match.assignNotice"),
          "warning",
          "triangle-alert",
        )
      : null,
  );
}

function draftOf(configured, preset) {
  const source = configured.source;
  return {
    id: source.id,
    name: preset && source.name === preset.name ? "" : source.name,
    url: preset && source.url === preset.url ? "" : source.url,
    timezone: preset && source.timezone === preset.timezone ? "" : source.timezone,
    proxy: source.proxy || "auto",
    enabled: configured.enabled,
  };
}

function persistSource(body, revision) {
  if (revision > 0) return endpoints.updateEPGSource(body.id, body, revision);
  return endpoints.createEPGSource(body);
}

function proxyChoices(proxies) {
  return [
    ["auto", i18n.t("epg.proxy.auto")],
    ["direct", i18n.t("epg.proxy.direct")],
    ...proxies.filter((proxy) => !proxy.disabled).map((proxy) => [proxy.id, `${proxy.id} · ${proxy.name || i18n.t("epg.proxy.generic")}`]),
  ];
}

function openSourceModal({ presets, proxies, configured = null, existing = [], after }) {
  const isNew = !configured;
  const source = configured?.source || {};
  const copy = sourceCopy(source);
  const preset = presets.get(source.id) || null;

  const idInput = input("id", source.id || "", { required: true, disabled: !isNew, placeholder: "guide-source" });
  const nameInput = input("name", source.name || "", { placeholder: i18n.t("epg.form.namePlaceholder") });
  const urlInput = input("url", source.url || "", { type: "url", required: true, placeholder: "https://example.com/epg.xml.gz" });
  const timezoneInput = input("timezone", source.timezone || "", { placeholder: "UTC" });
  const proxySelect = select("proxy", proxyChoices(proxies), source.proxy || "auto");
  const enabledToggle = h("input", { type: "checkbox", id: "epg-source-enabled", checked: isNew ? true : Boolean(configured.enabled) });

  const submit = button(i18n.t(isNew ? "epg.form.add" : "epg.form.save"), {
    kind: "primary",
    onClick: async () => {
      const id = idInput.value.trim() || source.id;
      const url = urlInput.value.trim();
      for (const control of [idInput, urlInput]) control.removeAttribute("aria-invalid");
      if (!id) {
        idInput.setAttribute("aria-invalid", "true");
        idInput.focus();
        toast(i18n.t("epg.form.idRequired"), "", "danger");
        return;
      }
      if (isNew && existing.includes(id)) {
        idInput.setAttribute("aria-invalid", "true");
        idInput.focus();
        toast(i18n.t("epg.form.idExists"), i18n.t("epg.form.idExistsDescription"), "danger");
        return;
      }
      if (!url && !preset) {
        urlInput.setAttribute("aria-invalid", "true");
        urlInput.focus();
        toast(i18n.t("epg.form.urlRequired"), i18n.t("epg.form.urlRequiredDescription"), "danger");
        return;
      }

      submit.disabled = true;
      try {
        await persistSource(
          {
            id,
            name: preset && nameInput.value.trim() === preset.name ? "" : nameInput.value.trim(),
            url: preset && url === preset.url ? "" : url,
            timezone: preset && timezoneInput.value.trim() === preset.timezone ? "" : timezoneInput.value.trim(),
            proxy: proxySelect.value,
            enabled: enabledToggle.checked,
          },
          configured?.revision || 0,
        );
        closeModal();
        toast(i18n.t(isNew ? "epg.form.added" : "epg.form.saved"), i18n.t("epg.refreshHint"));
        await after();
      } catch (error) {
        toastError(error, i18n.t("epg.form.saveFailed"));
        submit.disabled = false;
      }
    },
  });

  openModal({
    title: isNew ? i18n.t("epg.form.addTitle") : i18n.t("epg.form.editTitle", { source: copy.name }),
    description: i18n.t(preset ? "epg.form.presetDescription" : "epg.form.customDescription"),
    body: h(
      "div",
      { class: "form-grid" },
      field(i18n.t("epg.form.id"), idInput, i18n.t(isNew ? "epg.form.idHintNew" : "epg.form.idHintEdit")),
      field(i18n.t("epg.form.name"), nameInput),
      h("div", { class: "span-all" }, field(i18n.t("epg.form.url"), urlInput, preset ? i18n.t("epg.form.presetDefault", { url: preset.url }) : "")),
      field(i18n.t("epg.form.timezone"), timezoneInput, i18n.t("epg.form.timezoneHint")),
      field(i18n.t("epg.form.egress"), proxySelect, i18n.t("epg.form.egressHint")),
      h(
        "div",
        { class: "span-all" },
        h("label", { class: "check-row", htmlFor: "epg-source-enabled" }, enabledToggle, h("span", { text: i18n.t("epg.form.enable") })),
      ),
    ),
    actions: [button(i18n.t("shared.cancel"), { onClick: closeModal }), submit],
  });
}
