import { frag, h, icon } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { invalidateCatalog, loadCatalog, refreshStatus, sessionFor, sourceURL, store } from "/admin/assets/core/store.js";
import { badge, button, card, channelAvatar, emptyState, field, formSection, iconButton, input, linkButton, pageHead, select, stateBadge } from "/admin/assets/ui/kit.js";
import { closeModal, confirmDialog, copyText, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";
import { matchesQuery } from "/admin/assets/views/channels.js";
import { matchBadge } from "/admin/assets/views/epg.js";
import { previewChannel } from "/admin/assets/views/preview.js";

const BLANK = {
  id: "", title: "", group: "", logo_url: "", upstream: "", path: "", ingress: "hls",
  disabled: false, on_demand: true, autostart: false, idle_timeout_sec: 90,
  keys: "", keys_file: "", user_agent: "", headers: {}, restart_on_failure: false, prefer_height: 0,
  packager: "", epg_id: "", epg_name: "", epg_source: "",
};

export async function renderChannelDetail(ctx) {
  await loadCatalog({ signal: ctx.signal });
  const isNew = ctx.id === "new";

  let channel = { ...BLANK, upstream: store.upstreams.length === 1 ? store.upstreams[0].id : "" };
  let revision = 0;

  if (!isNew) {
    const detail = await endpoints.channel(ctx.id, ctx.signal);
    if (!ctx.alive()) return frag();
    channel = { ...BLANK, ...detail.channel };
    revision = detail.revision || 0;
  }

  const [matchData, sourceData] = await Promise.all([endpoints.epgMatches(ctx.signal), endpoints.epgSources(ctx.signal)]);
  if (!ctx.alive()) return frag();
  const epg = {
    match: (matchData.matches || []).find((item) => item.channel_id === channel.id) || null,
    sources: (sourceData.sources || []).map((item) => item.source),
  };

  await refreshStatus();
  if (!ctx.alive()) return frag();

  const editor = buildEditor(ctx, channel, revision, isNew, epg);
  return frag(
    pageHead(isNew ? "添加频道" : channel.title || channel.id, isNew ? "按顺序填写频道信息、节目源与运行方式。" : "检查节目源、调整配置并执行常用运行操作。", [
      linkButton("全部频道", "/admin/channels", { iconName: "chevron-left" }),
    ]),
    h("div", { class: "workspace" }, buildPicker(channel.id, isNew), editor),
  );
}

function buildPicker(currentID, isNew) {
  const filter = input("picker_filter", "", { placeholder: "筛选频道…", type: "search" });
  filter.setAttribute("aria-label", "筛选频道");
  const list = h("nav", { class: "picker-list", "aria-label": "频道列表" });

  const draw = () => {
    const visible = store.channels.filter((channel) => matchesQuery(channel, filter.value));
    const items = visible.map((channel) =>
      h(
        "a",
        {
          class: "picker-item",
          href: `/admin/channels/${encodeURIComponent(channel.id)}`,
          "data-route": true,
          "aria-current": !isNew && channel.id === currentID ? "page" : null,
        },
        channelAvatar(channel, 32),
        h("span", { class: "identity-copy" }, h("strong", { text: channel.title || channel.id }), h("small", { class: "mono", text: channel.id })),
      ),
    );
    if (isNew) {
      items.unshift(
        h(
          "a",
          { class: "picker-item", href: "/admin/channels/new", "data-route": true, "aria-current": "page" },
          h("span", { class: "avatar avatar-channel", style: { width: "32px", height: "32px" } }, icon("plus", 16)),
          h("span", { class: "identity-copy" }, h("strong", { text: "添加频道" }), h("small", { text: "尚未创建" })),
        ),
      );
    }
    if (!items.length) items.push(emptyState("没有匹配的频道", ""));
    list.replaceChildren(...items);
  };

  filter.addEventListener("input", draw);
  draw();

  return h(
    "aside",
    { class: "picker" },
    card({
      title: "频道",
      description: `${store.channels.length} 个频道`,
      action: h("a", { class: "icon-button is-outline", href: "/admin/channels/new", "data-route": true, title: "添加频道", "aria-label": "添加频道" }, icon("plus", 18)),
      body: frag(filter, list),
    }),
  );
}

function buildEditor(ctx, channel, revision, isNew, epg) {
  const form = h("form", { class: "channel-form", novalidate: true });

  const idInput = input("id", channel.id, { required: true, disabled: !isNew, placeholder: "channel-1" });
  const titleInput = input("title", channel.title, { required: true });
  const groupInput = input("group", channel.group);
  const logoInput = input("logo_url", channel.logo_url, { type: "url", placeholder: "https://…" });

  const epgIDInput = input("epg_id", channel.epg_id, { placeholder: "456556" });
  const epgNameInput = input("epg_name", channel.epg_name, { placeholder: "頻道名稱" });
  const epgSourceSelect = select("epg_source", epgSourceChoices(epg.sources, channel.epg_source), channel.epg_source || "");

  const upstreamSelect = select(
    "upstream",
    [["", "选择来源服务器"], ...store.upstreams.map((item) => [item.id, `${item.id} · ${item.base_url}`])],
    channel.upstream,
  );
  upstreamSelect.required = true;
  const pathInput = input("path", channel.path, { required: true, placeholder: "/live/channel/index.m3u8" });

  const ingressSelect = select("ingress", [["hls", "HLS（.m3u8）"], ["dash", "DASH（.mpd）"]], channel.ingress);
  const strategy = channel.autostart ? (channel.on_demand ? "prewarm" : "persistent") : "ondemand";
  const strategySelect = select("strategy", [
    ["ondemand", "有观众时启动"],
    ["persistent", "始终运行"],
    ["prewarm", "启动时预热，无观众后停止"],
  ], strategy);
  const idleInput = input("idle_timeout_sec", channel.idle_timeout_sec || 90, { type: "number", min: 10 });
  const heightInput = input("prefer_height", channel.prefer_height || 0, { type: "number", min: 0 });
  const packagerSelect = select("packager", [
    ["", "跟随全局设置"],
    ["auto", "自动选择（优先原生）"],
    ["native", "仅原生（不支持则失败）"],
    ["ffmpeg", "仅 ffmpeg"],
  ], channel.packager || "");
  const keysInput = h("textarea", {
    name: "keys",
    rows: 3,
    spellcheck: "false",
    autocomplete: "off",
    placeholder: "ffeeddccbbaa99887766554433221100:00112233445566778899aabbccddeeff",
    value: channel.keys || "",
  });
  keysInput.classList.add("mono");
  const userAgentInput = input("user_agent", channel.user_agent, { placeholder: `自动使用 Kiln/${store.version || "当前版本"}` });
  const restartSelect = select("restart_on_failure", [["false", "不自动重启"], ["true", "失败后自动重启"]], String(Boolean(channel.restart_on_failure)));
  const headersInput = h("textarea", {
    name: "headers",
    rows: 4,
    placeholder: "Header-Name: value",
    value: Object.entries(channel.headers || {}).map(([key, value]) => `${key}: ${value}`).join("\n"),
  });

  const sourceValue = h("span", { class: "mono truncate", text: sourceURL(channel.upstream, channel.path) });
  const syncSource = () => {
    sourceValue.textContent = sourceURL(upstreamSelect.value, pathInput.value);
  };
  upstreamSelect.addEventListener("change", syncSource);
  pathInput.addEventListener("input", syncSource);

  const keysPanel = h("div", { class: "panel span-all" }, field(
    "DRM 密钥",
    keysInput,
    "DASH 频道必填。每行一组 KID:KEY，均为 32 位十六进制。密钥保存后不再回传，页面只显示 KID；不改动此处即保留原有密钥。",
  ));
  const packagerField = field("封装引擎", packagerSelect, "仅对 DASH 频道生效。原生引擎不启动外部进程，直接输出 HLS fMP4。");
  const syncIngress = () => {
    const isDash = ingressSelect.value === "dash";
    keysPanel.hidden = !isDash;
    packagerField.hidden = !isDash;
    restartSelect.disabled = isDash;
    if (isDash) restartSelect.value = "true";
    pathInput.placeholder = isDash ? "/live/channel/manifest.mpd" : "/live/channel/index.m3u8";
  };
  ingressSelect.addEventListener("change", syncIngress);
  syncIngress();

  const dirtyLabel = h("span", { class: "muted", text: "没有未保存更改" });
  const saveButton = button(isNew ? "创建频道" : "保存更改", { kind: "primary", type: "submit", iconName: "check" });

  const onEdit = () => {
    ctx.markDirty(true);
    dirtyLabel.textContent = "有未保存更改";
    dirtyLabel.classList.add("is-dirty");
  };
  form.addEventListener("input", onEdit);
  form.addEventListener("change", onEdit);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const strategyValue = strategySelect.value;
    const ingress = ingressSelect.value;

    const headers = {};
    for (const line of headersInput.value.split("\n")) {
      const split = line.indexOf(":");
      if (split > 0) headers[line.slice(0, split).trim()] = line.slice(split + 1).trim();
    }

    const body = {
      id: idInput.value.trim() || channel.id,
      title: titleInput.value.trim(),
      group: groupInput.value.trim(),
      logo_url: logoInput.value.trim(),
      epg_id: epgIDInput.value.trim(),
      epg_name: epgNameInput.value.trim(),
      epg_source: epgSourceSelect.value,
      upstream: upstreamSelect.value,
      path: pathInput.value.trim(),
      ingress,
      disabled: Boolean(channel.disabled),
      on_demand: strategyValue !== "persistent",
      autostart: strategyValue !== "ondemand",
      idle_timeout_sec: Number(idleInput.value || 90),
      prefer_height: Number(heightInput.value || 0),
      packager: ingress === "dash" ? packagerSelect.value : "",
      keys: keysInput.value.trim(),
      keys_file: channel.keys_file || "",
      user_agent: userAgentInput.value.trim(),
      headers,
      restart_on_failure: ingress === "dash" || restartSelect.value === "true",
    };

    const required = [
      [idInput, body.id],
      [titleInput, body.title],
      [upstreamSelect, body.upstream],
      [pathInput, body.path],
      [keysInput, ingress !== "dash" || body.keys || body.keys_file],
    ];
    for (const [control] of required) control.removeAttribute("aria-invalid");
    const missing = required.filter(([, value]) => !value);
    if (missing.length) {
      for (const [control] of missing) control.setAttribute("aria-invalid", "true");
      missing[0][0].focus();
      toast("请完成必填项目", "已标出需要填写的频道配置。", "danger");
      return;
    }

    saveButton.disabled = true;
    try {
      if (isNew) await endpoints.createChannel(body);
      else await endpoints.updateChannel(channel.id, body, revision);
      ctx.markDirty(false);
      invalidateCatalog();
      toast(isNew ? "频道已创建" : "频道已保存");
      await ctx.navigate(`/admin/channels/${encodeURIComponent(body.id)}`);
      if (!isNew) await ctx.reload();
    } catch (error) {
      toastError(error, "保存失败");
      saveButton.disabled = false;
    }
  });

  const dormant = !isNew && channel.disabled && !epg.match;

  const applyCandidate = (candidate) => {
    epgIDInput.value = candidate.channel_id;
    epgNameInput.value = candidate.name || "";
    epgSourceSelect.value = hasSourceOption(epgSourceSelect, candidate.source_id) ? candidate.source_id : "";
    onEdit();
    toast("已回填节目单标识", `来自 ${candidate.source_id} 的 ${candidate.channel_id}。`);
  };

  const applyLogo = (logo) => {
    logoInput.value = logo.url;
    onEdit();
    toast("已回填台标地址", logo.source_id);
  };

  form.append(
    formSection("1", "频道信息", "设置频道在目录和播放器中显示的名称。",
      h("div", { class: "form-grid" },
        field("频道标识符（ID）", idInput, isNew ? "用于播放地址，创建后不可更改。" : "标识符创建后不可更改。"),
        field("频道名称", titleInput),
        field("频道分组", groupInput),
      ),
    ),
    formSection("2", "节目源", "来源服务器与节目源路径组合成下方的完整地址。",
      h("div", { class: "form-grid" },
        field("来源服务器", upstreamSelect),
        field("节目源路径", pathInput),
        h("div", { class: "source-preview span-all" },
          h("span", { class: "source-preview-label", text: "完整节目源地址" }),
          sourceValue,
          iconButton("copy", "复制完整节目源地址", { onClick: () => copyText(sourceValue.textContent, "节目源地址已复制") }),
        ),
      ),
    ),
    formSection("3", "运行方式", "选择流媒体格式、启动时机与播放偏好。",
      h("div", { class: "form-grid" },
        field("流媒体格式", ingressSelect),
        field("运行方式", strategySelect),
        field("无观众后停止（秒）", idleInput, "仅在允许自动停止时生效。"),
        field("首选视频高度", heightInput, "0 表示使用系统默认值。"),
        packagerField,
        keysPanel,
      ),
    ),
    formSection("4", "节目单与台标", "指定这个频道在 XMLTV 里的标识，播放器据此显示节目单。",
      h("div", { class: "form-grid" },
        h("div", { class: "span-all" },
          h("div", { class: "inline-row" },
            dormant ? badge("频道已停用", "neutral") : matchBadge(epg.match?.status),
            h("span", { class: "muted", text: dormant ? DORMANT_HINT : matchHint(epg.match) }),
            button("从节目单里找", {
              size: "small",
              iconName: "search",
              onClick: () => openCandidateModal(epg.match, applyCandidate, dormant),
            }),
            button("选择台标", {
              size: "small",
              iconName: "eye",
              onClick: () => openLogoModal(epg.match, applyLogo, dormant),
            }),
          ),
        ),
        field("节目单频道标识（tvg-id）", epgIDInput, "同名频道可能有多个标识，请从候选里挑选正确的一个。"),
        field("节目单频道名称", epgNameInput, "留空时使用频道名称参与匹配。"),
        field("限定节目单源", epgSourceSelect, "只在指定的源里匹配，可避免多个源互相覆盖。"),
        field("台标地址", logoInput, "留空时自动使用 Kiln 台标代理（/v1/logo/频道标识）。"),
      ),
    ),
    h("details", { class: "disclosure" },
      h("summary", {}, icon("sliders-horizontal", 18), h("span", { text: "高级请求设置" }), h("small", { text: "User-Agent、请求头与故障恢复" })),
      h("div", { class: "disclosure-body" },
        h("div", { class: "form-grid" },
          field("User-Agent", userAgentInput, "留空时自动随 Kiln 版本号更新。"),
          field("故障恢复", restartSelect, "DASH 频道始终自动重启。"),
          h("div", { class: "field span-all" },
            h("label", { class: "field-label", htmlFor: "channel-headers", text: "附加请求头" }),
            Object.assign(headersInput, { id: "channel-headers" }),
            h("p", { class: "field-hint", text: "每行一个请求头。敏感请求头保存后不会再次显示。" }),
          ),
        ),
      ),
    ),
    h("div", { class: "form-footer" },
      dirtyLabel,
      h("div", { class: "form-footer-actions" }, linkButton("取消", "/admin/channels"), saveButton),
    ),
  );

  return h("div", { class: "editor" }, buildCommandBar(ctx, channel, revision, isNew), card({ body: form, flush: true }), buildDangerZone(ctx, channel, revision, isNew));
}

const DORMANT_HINT = "频道已停用，不参与节目单匹配。启用后才会出现候选。";

function epgSourceChoices(sources, current) {
  const choices = [["", "不限定（在所有已启用的源里匹配）"], ...sources.map((source) => [source.id, `${source.id} · ${source.name}`])];
  if (current && !sources.some((source) => source.id === current)) choices.push([current, `${current} · 已失效的源`]);
  return choices;
}

function hasSourceOption(element, value) {
  return [...element.options].some((option) => option.value === value);
}

function matchHint(match) {
  if (!match) return "尚无匹配结果，请先在节目单页面启用源并刷新。";
  const count = (match.candidates || []).length;
  if (match.status === "matched") return `已锁定 ${match.match?.source_id || "节目单源"} 的 ${match.match?.channel_id || ""}。`;
  if (match.status === "suggested") return `找到 ${count} 个候选，需要人工确认。`;
  return "没有找到候选，可手动填写标识。";
}

function openCandidateModal(match, apply, dormant) {
  const candidates = match?.candidates || [];
  const body = candidates.length
    ? h(
        "div",
        { class: "list" },
        candidates.map((candidate) =>
          h(
            "div",
            { class: "list-item" },
            h(
              "span",
              {},
              h("strong", { text: candidate.name || candidate.channel_id }),
              h("small", { class: "mono", text: `${candidate.source_id} · ${candidate.channel_id}` }),
              candidate.names?.length > 1 ? h("small", { text: candidate.names.join("、") }) : null,
            ),
            button("使用", {
              size: "small",
              kind: "primary",
              onClick: () => {
                closeModal();
                apply(candidate);
              },
            }),
          ),
        ),
      )
    : emptyState("没有候选", dormant ? DORMANT_HINT : "请先在节目单页面启用节目单源并刷新，或手动填写节目单频道标识。");

  openModal({
    title: "从节目单里找",
    description: "同名频道可能对应多个标识，请逐条核对后手动选择，Kiln 不会替你决定。",
    body,
    actions: [button("关闭", { onClick: closeModal })],
  });
}

function openLogoModal(match, apply, dormant) {
  const logos = match?.logo_candidates || [];
  const body = logos.length
    ? h(
        "div",
        { class: "list" },
        logos.map((logo) =>
          h(
            "div",
            { class: "list-item" },
            h(
              "span",
              { class: "identity" },
              h("img", { class: "logo-preview", src: logo.url, alt: "", loading: "lazy" }),
              h(
                "span",
                { class: "identity-copy" },
                h("strong", { text: logo.name || logo.source_id }),
                h("small", { class: "mono", title: logo.url, text: logo.url }),
              ),
            ),
            button("使用", {
              size: "small",
              kind: "primary",
              onClick: () => {
                closeModal();
                apply(logo);
              },
            }),
          ),
        ),
      )
    : emptyState("没有候选台标", dormant ? DORMANT_HINT : "台标候选来自频道名称，填写节目单频道名称后再试一次。");

  openModal({
    title: "选择台标",
    description: "候选按优先级排列；加载不出来的图片说明该地址暂时不可用。",
    body,
    actions: [button("关闭", { onClick: closeModal })],
  });
}

function buildCommandBar(ctx, channel, revision, isNew) {
  const statusSlot = h("span", { class: "state-slot" }, isNew ? badge("尚未创建", "neutral") : stateBadge(channel, sessionFor(channel.id)));
  const actionSlot = h("div", { class: "command-actions" });

  const paint = () => {
    if (!ctx.alive()) return;
    const session = sessionFor(channel.id);
    if (!isNew) statusSlot.replaceChildren(stateBadge(channel, session));
    actionSlot.replaceChildren(
      ...(isNew
        ? []
        : [
            button("检查节目源", { iconName: "activity", onClick: () => probe(channel.id) }),
            button("立即启动", { iconName: "power", disabled: channel.disabled, onClick: () => warmup(channel.id) }),
            button("打开预览", { kind: "primary", iconName: "play", disabled: channel.disabled, onClick: () => previewChannel(channel) }),
            session ? iconButton("square", "停止当前会话", { kind: "danger", variant: "outline", onClick: () => stop(ctx, channel.id) }) : null,
          ].filter(Boolean)),
    );
  };

  paint();
  if (!isNew) ctx.watchStatus(paint);

  return h(
    "div",
    { class: "command-bar" },
    h(
      "div",
      { class: "identity" },
      channelAvatar(channel, 40),
      h(
        "div",
        { class: "identity-copy" },
        h("strong", { text: isNew ? "添加频道" : channel.title || channel.id }),
        h("small", { class: "mono", text: isNew ? "填写下方配置后创建" : channel.id }),
      ),
      statusSlot,
    ),
    actionSlot,
  );
}

function buildDangerZone(ctx, channel, revision, isNew) {
  if (isNew) return null;

  const toggle = async () => {
    try {
      const detail = await endpoints.channel(channel.id);
      const next = { ...detail.channel, disabled: !detail.channel.disabled };
      await endpoints.updateChannel(channel.id, next, detail.revision);
      ctx.markDirty(false);
      invalidateCatalog();
      toast(next.disabled ? "频道已停用" : "频道已启用");
      await ctx.reload();
    } catch (error) {
      toastError(error, "状态更新失败");
    }
  };

  const remove = async () => {
    const accepted = await confirmDialog({
      title: "永久删除频道？",
      description: "删除后频道配置无法恢复。",
      warning: "正在进行的会话会立即中断，播放地址随即失效。",
      confirmLabel: "永久删除",
      expect: channel.id,
    });
    if (!accepted) return;
    try {
      const detail = await endpoints.channel(channel.id);
      await endpoints.deleteChannel(channel.id, detail.revision);
      ctx.markDirty(false);
      invalidateCatalog();
      toast("频道已删除");
      await ctx.navigate("/admin/channels");
    } catch (error) {
      toastError(error, "删除失败");
    }
  };

  return card({
    title: "频道管理",
    tone: "danger",
    body: h(
      "div",
      { class: "list" },
      h(
        "div",
        { class: "list-item" },
        h("span", {}, h("strong", { text: channel.disabled ? "启用频道" : "停用频道" }), h("small", { text: channel.disabled ? "恢复目录展示与播放访问。" : "从目录隐藏并停止现有会话。" })),
        button(channel.disabled ? "启用" : "停用", { kind: channel.disabled ? "secondary" : "danger", size: "small", onClick: toggle }),
      ),
      h(
        "div",
        { class: "list-item" },
        h("span", {}, h("strong", { text: "删除频道" }), h("small", { text: "永久移除频道配置，此操作无法撤销。" })),
        button("删除", { kind: "danger", size: "small", onClick: remove }),
      ),
    ),
  });
}

async function probe(id) {
  toast("正在检查", "正在连接节目源…", "info");
  try {
    const result = await endpoints.probeChannel(id);
    if (result.ok) toast("节目源正常", `HTTP ${result.status} · ${result.dur_ms} ms`);
    else toast("节目源无响应", result.error || "上游没有返回有效响应", "danger");
  } catch (error) {
    toastError(error, "检查失败");
  }
}

async function warmup(id) {
  try {
    await endpoints.warmupChannel(id);
    await refreshStatus();
    toast("已开始启动", "可在总览页查看运行状态。");
  } catch (error) {
    toastError(error, "无法启动频道");
  }
}

async function stop(ctx, id) {
  const accepted = await confirmDialog({
    title: "停止会话？",
    description: `频道 ${id} 的打包进程会立即停止，下次播放需要重新冷启动。`,
    warning: "正在观看的播放器会立即中断。",
    confirmLabel: "停止会话",
  });
  if (!accepted) return;
  try {
    await endpoints.stopSession(id);
    await refreshStatus();
    toast("会话已停止");
  } catch (error) {
    toastError(error, "停止失败");
  }
}
