import { formatBytes, formatISOTime, formatNumber, frag, h } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { badge, button, card, emptyState, field, iconButton, input, notice, pageHead, select, table } from "/admin/assets/ui/kit.js";
import { closeModal, confirmDialog, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";

const ID_KINDS = {
  numeric: "数字 ID",
  name: "频道名 ID",
  mixed: "混合 ID",
};

const MATCH_TONES = {
  matched: ["已匹配", "success"],
  suggested: ["待确认", "warning"],
  unmatched: ["未匹配", "danger"],
};

export function matchBadge(status) {
  if (!status) return badge("节目单未启用", "neutral");
  const [label, tone] = MATCH_TONES[status] || ["未知", "neutral"];
  return badge(label, tone);
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

  const enabledCount = sources.filter((item) => item.enabled).length;
  const dormant = enabledCount > 0 && statuses.size === 0;

  const refreshButton = button("立即刷新", {
    kind: "primary",
    iconName: "refresh-cw",
    onClick: async () => {
      refreshButton.disabled = true;
      toast("正在刷新", "正在向已启用的节目单源拉取数据…", "info");
      try {
        const result = await endpoints.refreshEPG();
        const failed = (result.statuses || []).filter((status) => status.error);
        if (result.ok && !failed.length) toast("节目单已刷新", `${(result.statuses || []).length} 个源全部成功。`);
        else toast("刷新完成，但有源失败", `${failed.length} 个源返回错误，详情见下方状态。`, "warning");
        await ctx.reload();
      } catch (error) {
        toastError(error, "刷新失败");
        refreshButton.disabled = false;
      }
    },
  });

  const reload = async () => {
    if (ctx.alive()) await ctx.reload();
  };

  const sourceBody = sources.length
    ? table(
        ["启用", "节目单源", "地址", "标识与时区", "运行状态", ""],
        sources.map((configured) => sourceRow(configured, presets, statuses, proxies, reload)),
      )
    : emptyState("没有可用的节目单源", "内置预设未能加载，请检查服务端版本。");

  return frag(
    pageHead("节目单", "启用节目单源并刷新数据，Kiln 会把它们合并为一份 XMLTV 提供给播放器。", [refreshButton]),
    h(
      "div",
      { class: "stack" },
      dormant
        ? notice("已勾选节目单源，但服务端尚无任何抓取记录。请确认配置文件中 epg.enabled 为 true，然后点击「立即刷新」。", "warning", "triangle-alert")
        : null,
      card({
        title: "节目单源",
        description: "勾选即启用。预设源可直接使用，也可以覆盖其地址、时区与出口。",
        body: sourceBody,
        flush: true,
        action: button("添加自定义源", {
          size: "small",
          iconName: "plus",
          onClick: () => openSourceModal({ presets, proxies, existing: sources.map((item) => item.source.id), after: reload }),
        }),
      }),
      card({
        title: "频道匹配概览",
        description: "在频道页面为「待确认」与「未匹配」的频道手动指定节目单。",
        body: matchSummary(matches),
      }),
    ),
  );
}

function sourceRow(configured, presets, statuses, proxies, after) {
  const source = configured.source;
  const preset = presets.get(source.id) || null;
  const status = statuses.get(source.id) || null;

  const toggle = h("input", {
    type: "checkbox",
    checked: configured.enabled,
    "aria-label": `启用节目单源 ${source.name || source.id}`,
  });
  toggle.addEventListener("change", async () => {
    toggle.disabled = true;
    try {
      await persistSource({ ...draftOf(configured, preset), enabled: toggle.checked }, configured.revision);
      toast(toggle.checked ? "节目单源已启用" : "节目单源已停用", toggle.checked ? "点击「立即刷新」拉取最新数据。" : "");
      await after();
    } catch (error) {
      toggle.checked = !toggle.checked;
      toggle.disabled = false;
      toastError(error, "更新失败");
    }
  });

  const remove = async () => {
    const accepted = await confirmDialog({
      title: preset ? "恢复预设默认值？" : "删除自定义源？",
      description: preset
        ? `将清除对 ${source.name} 的所有覆盖，并停用该源。`
        : `节目单源 ${source.name || source.id} 会被永久移除。`,
      confirmLabel: preset ? "恢复默认" : "删除",
    });
    if (!accepted) return;
    try {
      await endpoints.deleteEPGSource(source.id, configured.revision);
      toast(preset ? "已恢复预设默认值" : "自定义源已删除");
      await after();
    } catch (error) {
      toastError(error, "删除失败");
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
        h("strong", { text: source.name || source.id }),
        h("small", { class: "mono", text: source.id }),
        source.description ? h("small", { text: source.description }) : null,
      ),
    ),
    h(
      "td",
      {},
      h(
        "div",
        { class: "source-cell" },
        h("span", { class: "mono truncate", text: source.url || "未填写地址" }),
        h("small", { text: source.approx_bytes ? `实测约 ${formatBytes(source.approx_bytes)}` : "体积未知" }),
      ),
    ),
    h(
      "td",
      {},
      h(
        "div",
        { class: "badge-row" },
        badge(source.region || "custom", "neutral"),
        badge(ID_KINDS[source.id_kind] || source.id_kind || "未知", "neutral"),
        badge(source.timezone || "默认时区", "neutral"),
        badge(`出口 ${source.proxy || "auto"}`, "neutral"),
      ),
    ),
    h("td", {}, statusCell(configured.enabled, status)),
    h(
      "td",
      {},
      h(
        "div",
        { class: "row-actions" },
        iconButton("pencil", `编辑 ${source.name || source.id}`, {
          variant: "outline",
          onClick: () => openSourceModal({ presets, proxies, configured, after }),
        }),
        iconButton("trash-2", preset ? `恢复 ${source.name} 的默认值` : `删除 ${source.name || source.id}`, {
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
  if (!enabled) return h("span", { class: "muted", text: "未启用" });
  if (!status) return badge("等待刷新", "warning");

  const badges = h("div", { class: "badge-row" });
  if (status.error) badges.append(badge("抓取失败", "danger", "circle-alert"));
  else if (status.stale) badges.append(badge("数据过期", "warning"));
  else if (status.available) badges.append(badge("正常", "success", "circle-check"));
  else badges.append(badge("暂无数据", "neutral"));

  return h(
    "div",
    { class: "source-cell" },
    badges,
    h("small", { text: `频道 ${formatNumber(status.channel_count)} · 节目 ${formatNumber(status.programme_count)}` }),
    h("small", { text: `上次成功 ${formatISOTime(status.last_success)}` }),
    status.error ? h("small", { class: "text-danger truncate", title: status.error, text: status.error }) : null,
  );
}

function matchSummary(matches) {
  if (!matches.length) {
    return emptyState("暂无匹配结果", "启用至少一个节目单源并完成一次刷新后，这里会显示频道的匹配情况。");
  }
  const counts = { matched: 0, suggested: 0, unmatched: 0 };
  for (const item of matches) {
    if (item.status in counts) counts[item.status] += 1;
  }
  return h(
    "div",
    { class: "badge-row" },
    badge(`已匹配 ${counts.matched}`, counts.matched ? "success" : "neutral"),
    badge(`待确认 ${counts.suggested}`, counts.suggested ? "warning" : "neutral"),
    badge(`未匹配 ${counts.unmatched}`, counts.unmatched ? "danger" : "neutral"),
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
    ["auto", "auto · 跟随全局出口设置"],
    ["direct", "direct · 直接连接"],
    ...proxies.filter((proxy) => !proxy.disabled).map((proxy) => [proxy.id, `${proxy.id} · ${proxy.name || "代理"}`]),
  ];
}

function openSourceModal({ presets, proxies, configured = null, existing = [], after }) {
  const isNew = !configured;
  const source = configured?.source || {};
  const preset = presets.get(source.id) || null;

  const idInput = input("id", source.id || "", { required: true, disabled: !isNew, placeholder: "my-epg" });
  const nameInput = input("name", source.name || "", { placeholder: "自定义节目单源" });
  const urlInput = input("url", source.url || "", { type: "url", required: true, placeholder: "https://example.com/epg.xml.gz" });
  const timezoneInput = input("timezone", source.timezone || "", { placeholder: "Asia/Shanghai" });
  const proxySelect = select("proxy", proxyChoices(proxies), source.proxy || "auto");
  const enabledToggle = h("input", { type: "checkbox", id: "epg-source-enabled", checked: isNew ? true : Boolean(configured.enabled) });

  const submit = button(isNew ? "添加" : "保存", {
    kind: "primary",
    onClick: async () => {
      const id = idInput.value.trim() || source.id;
      const url = urlInput.value.trim();
      for (const control of [idInput, urlInput]) control.removeAttribute("aria-invalid");
      if (!id) {
        idInput.setAttribute("aria-invalid", "true");
        idInput.focus();
        toast("请填写源标识符", "", "danger");
        return;
      }
      if (isNew && existing.includes(id)) {
        idInput.setAttribute("aria-invalid", "true");
        idInput.focus();
        toast("源标识符已存在", "请换一个标识符，或直接编辑同名的源。", "danger");
        return;
      }
      if (!url && !preset) {
        urlInput.setAttribute("aria-invalid", "true");
        urlInput.focus();
        toast("请填写节目单地址", "自定义源必须提供 XMLTV 地址。", "danger");
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
        toast(isNew ? "节目单源已添加" : "节目单源已保存", "点击「立即刷新」拉取最新数据。");
        await after();
      } catch (error) {
        toastError(error, "保存失败");
        submit.disabled = false;
      }
    },
  });

  openModal({
    title: isNew ? "添加自定义节目单源" : `编辑 ${source.name || source.id}`,
    description: preset ? "这是内置预设源，留空的项目会沿用预设默认值。" : "提供一个可访问的 XMLTV 地址，支持 .xml 与 .xml.gz。",
    body: h(
      "div",
      { class: "form-grid" },
      field("源标识符", idInput, isNew ? "用于区分不同来源，添加后不可更改。" : "标识符不可更改。"),
      field("显示名称", nameInput),
      h("div", { class: "span-all" }, field("节目单地址", urlInput, preset ? `预设默认值：${preset.url}` : "")),
      field("时区", timezoneInput, "IANA 时区名称，留空使用系统默认。"),
      field("网络出口", proxySelect, "节目单源常有地区限制，可指定代理绕开。"),
      h(
        "div",
        { class: "span-all" },
        h("label", { class: "check-row", htmlFor: "epg-source-enabled" }, enabledToggle, h("span", { text: "启用这个节目单源" })),
      ),
    ),
    actions: [button("取消", { onClick: closeModal }), submit],
  });
}
