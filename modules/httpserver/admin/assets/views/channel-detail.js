import { frag, h, icon } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { isValidSourceURL, resolveSourceFields } from "/admin/assets/core/source-url.js";
import { audioTrackLabel, choiceFromSelection, customSelector, selectorForTrack, subtitleTrackLabel, trackSummary, videoTrackLabel } from "/admin/assets/core/track-options.js";
import { invalidateCatalog, loadCatalog, refreshStatus, sessionFor, sourceURL, store } from "/admin/assets/core/store.js";
import { vt } from "/admin/assets/core/view-i18n.js";
import { badge, button, card, channelAvatar, emptyState, field, formSection, iconButton, input, linkButton, pageHead, select, stateBadge } from "/admin/assets/ui/kit.js";
import { closeModal, confirmDialog, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";
import { matchesQuery } from "/admin/assets/views/channels.js";
import { matchBadge } from "/admin/assets/views/epg.js";
import { previewChannel } from "/admin/assets/views/preview.js";

const BLANK = {
  id: "", title: "", group: "", logo_url: "", source_url: "", upstream: "", path: "", ingress: "hls",
  disabled: false, on_demand: true, autostart: false, idle_timeout_sec: 90,
  max_viewers: 0,
  user_agent: "", headers: {}, restart_on_failure: false, prefer_height: 0,
  preferred_audio_languages: [], selection: { video: { mode: "auto" }, audio: { mode: "auto" }, subtitles: { mode: "auto" } },
  packager: "", epg_id: "", epg_name: "", epg_source: "",
};

export async function renderChannelDetail(ctx) {
  const isNew = !ctx.id && ctx.query.get("new") === "1";
  const [, detail, matchData, sourceData, egressData] = await Promise.all([
    loadCatalog({ signal: ctx.signal }),
    isNew ? null : endpoints.channel(ctx.id, ctx.signal),
    endpoints.epgMatches(ctx.signal),
    endpoints.epgSources(ctx.signal),
    endpoints.egress(ctx.signal),
    refreshStatus(),
  ]);
  if (!ctx.alive()) return frag();

  let channel = { ...BLANK };
  let revision = 0;
  if (detail) {
    channel = { ...BLANK, ...detail.channel };
    revision = detail.revision || 0;
  }

  const epg = {
    match: (matchData.matches || []).find((item) => item.channel_id === channel.id) || null,
    sources: (sourceData.sources || []).map((item) => item.source),
  };

  const editor = buildEditor(ctx, channel, revision, isNew, epg, {
    ...(detail?.egress_binding || { mode: "auto" }),
    profiles: (egressData?.proxies || []).filter((profile) => !profile.disabled),
  });
  return frag(
    pageHead(isNew ? vt("channels.add") : channel.title || channel.id, vt(isNew ? "channel.newDescription" : "channel.editDescription"), [
      linkButton(vt("channel.all"), "/admin/channels", { iconName: "chevron-left" }),
    ]),
    h("div", { class: "workspace" }, buildPicker(channel.id, isNew), editor),
  );
}

function buildPicker(currentID, isNew) {
  const filter = input("picker_filter", "", { placeholder: vt("channels.filter"), type: "search" });
  filter.setAttribute("aria-label", vt("channels.filterAria"));
  const list = h("nav", { class: "picker-list", "aria-label": vt("channel.list") });

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
          { class: "picker-item", href: "/admin/channels?new=1", "data-route": true, "aria-current": "page" },
          h("span", { class: "avatar avatar-channel", style: { width: "32px", height: "32px" } }, icon("plus", 16)),
          h("span", { class: "identity-copy" }, h("strong", { text: vt("channels.add") }), h("small", { text: vt("channel.notCreated") })),
        ),
      );
    }
    if (!items.length) items.push(emptyState(vt("channels.noMatch"), ""));
    list.replaceChildren(...items);
  };

  filter.addEventListener("input", draw);
  draw();

  return h(
    "aside",
    { class: "picker" },
    card({
      title: vt("channels.title"),
      description: vt("common.channels", { count: store.channels.length }),
      action: h("a", { class: "icon-button is-outline", href: "/admin/channels?new=1", "data-route": true, title: vt("channels.add"), "aria-label": vt("channels.add") }, icon("plus", 18)),
      body: frag(filter, list),
    }),
  );
}

function buildEditor(ctx, channel, revision, isNew, epg, egress) {
  const form = h("form", { class: "channel-form", novalidate: true });

  const idInput = input("id", channel.id, {
    required: true,
    disabled: !isNew,
    placeholder: "channel-1",
    maxlength: 64,
    pattern: "[\\p{L}\\p{N}][\\p{L}\\p{N}._-]{0,63}",
  });
  const titleInput = input("title", channel.title, { required: true });
  const groupInput = input("group", channel.group);
  const logoInput = input("logo_url", channel.logo_url, { type: "url", placeholder: "https://…" });

  const epgIDInput = input("epg_id", channel.epg_id, { placeholder: "channel-id" });
  const epgNameInput = input("epg_name", channel.epg_name, { placeholder: vt("channel.epgNamePlaceholder") });
  const epgSourceSelect = select("epg_source", epgSourceChoices(epg.sources, channel.epg_source), channel.epg_source || "");

  const existingSourceURL = channel.source_url || (channel.upstream ? sourceURL(channel.upstream, channel.path) : channel.path);
  const sourceInput = input("source_url", existingSourceURL, {
    type: "url",
    required: true,
    placeholder: "https://example.com/live/channel/index.m3u8",
  });

  const ingressSelect = select("ingress", [["hls", "HLS（.m3u8）"], ["dash", "DASH（.mpd）"]], channel.ingress);
  const strategy = channel.autostart ? (channel.on_demand ? "prewarm" : "persistent") : "ondemand";
  const strategySelect = select("strategy", [
    ["ondemand", vt("run.ondemand")],
    ["persistent", vt("run.persistent")],
    ["prewarm", vt("run.prewarm")],
  ], strategy);
  const idleInput = input("idle_timeout_sec", channel.idle_timeout_sec || 90, { type: "number", min: 10 });
  const heightInput = input("prefer_height", channel.prefer_height || 0, { type: "number", min: 0 });
  const maxViewersInput = input("max_viewers", channel.max_viewers || 0, { type: "number", min: 0 });
  const preferredAudioInput = input("preferred_audio_languages", (channel.preferred_audio_languages || []).join(", "), { placeholder: "yue, zh, en" });
  const packagerSelect = select("packager", [
    ["", vt("channel.packagerGlobal")],
    ["auto", vt("channel.packagerAuto")],
    ["native", vt("channel.packagerNative")],
    ["ffmpeg", vt("channel.packagerFFmpeg")],
  ], channel.packager || "");
  const userAgentInput = input("user_agent", channel.user_agent, { placeholder: vt("channel.uaAuto", { version: store.version || "—" }) });
  const restartSelect = select("restart_on_failure", [["false", vt("channel.restartNo")], ["true", vt("channel.restartYes")]], String(Boolean(channel.restart_on_failure)));
  const headersInput = h("textarea", {
    name: "headers",
    rows: 4,
    placeholder: "Header-Name: value",
    value: Object.entries(channel.headers || {}).map(([key, value]) => `${key}: ${value}`).join("\n"),
  });

  const initialEgressValue = egress.mode === "direct"
    ? "direct"
    : egress.mode === "profile" && egress.profile_id ? `profile:${egress.profile_id}` : "auto";
  const egressChoices = [
    ["auto", vt("channel.egressAuto")],
    ["direct", vt("channel.egressDirect")],
    ...(egress.profiles || []).map((profile) => [`profile:${profile.id}`, `${profile.name || profile.id} · ${proxyKind(profile.url)}`]),
  ];
  if (initialEgressValue.startsWith("profile:") && !egressChoices.some(([value]) => value === initialEgressValue)) {
    egressChoices.push([initialEgressValue, vt("channel.egressUnavailable", { id: egress.profile_id })]);
  }
  egressChoices.push(["quick", vt("channel.egressQuick")]);
  const egressSelect = select("egress", egressChoices, initialEgressValue);
  const quickProxyName = input("quick_proxy_name", "", { placeholder: vt("channel.egressQuickNamePlaceholder") });
  const quickProxyURL = input("quick_proxy_url", "", { placeholder: "socks5h://user:password@host:1080" });
  const egressStatus = h("p", { class: "field-hint", text: vt("channel.egressTestHint") });
  const egressTestButton = button(vt("channel.egressTest"), { iconName: "activity" });
  const quickProxyPanel = h(
    "div",
    { class: "panel span-all" },
    h("div", { class: "form-grid" },
      field(vt("channel.egressQuickURL"), quickProxyURL, vt("channel.egressQuickURLHint")),
      field(vt("channel.egressQuickName"), quickProxyName, vt("channel.egressQuickNameHint")),
    ),
  );

  const currentEgress = () => {
    if (egressSelect.value === "auto" || egressSelect.value === "direct") return { mode: egressSelect.value };
    if (egressSelect.value === "quick") {
      return {
        mode: "profile",
        new_proxy: { name: quickProxyName.value.trim(), url: quickProxyURL.value.trim() },
      };
    }
    return { mode: "profile", profile_id: egressSelect.value.replace(/^profile:/, "") };
  };
  const selectedProxyURL = () => {
    if (egressSelect.value === "quick") return quickProxyURL.value.trim();
    if (!egressSelect.value.startsWith("profile:")) return "";
    return egress.profiles.find((profile) => profile.id === egressSelect.value.slice(8))?.url || "";
  };
  const syncEgress = () => {
    quickProxyPanel.hidden = egressSelect.value !== "quick";
    egressStatus.textContent = egressSelect.value === "auto"
      ? vt("channel.egressAutoHint")
      : egressSelect.value === "direct" ? vt("channel.egressDirectHint") : vt("channel.egressProfileHint");
  };
  egressSelect.addEventListener("change", syncEgress);
  syncEgress();

  egressTestButton.addEventListener("click", async () => {
    const route = currentEgress();
    if (route.new_proxy && !route.new_proxy.url) {
      quickProxyURL.setAttribute("aria-invalid", "true");
      quickProxyURL.focus();
      toast(vt("channel.egressURLRequired"), vt("channel.egressURLRequiredHint"), "danger");
      return;
    }
    quickProxyURL.removeAttribute("aria-invalid");
    egressTestButton.disabled = true;
    egressStatus.textContent = vt("channel.egressTesting");
    try {
      const result = await endpoints.testEgress({
        target: "bing",
        channel_id: idInput.value.trim() || channel.id,
        proxy_id: route.mode === "direct" ? "direct" : route.profile_id || "",
        proxy_url: route.new_proxy?.url || "",
      });
      const via = result.via_proxy || result.proxy_id || "direct";
      egressStatus.textContent = result.ok
        ? vt("channel.egressTestOK", { via, status: result.status, duration: result.dur_ms })
        : vt("channel.egressTestFailed", { reason: result.error || result.outcome || vt("common.unknown") });
      egressStatus.className = `field-hint ${result.ok ? "text-success" : "text-danger"}`;
    } catch (error) {
      egressStatus.textContent = error.message || vt("channel.egressTestFailed", { reason: vt("common.unknown") });
      egressStatus.className = "field-hint text-danger";
    } finally {
      egressTestButton.disabled = false;
    }
  });

  const initialSelection = channel.selection || BLANK.selection;
  const initialChoice = (kind) => choiceFromSelection(kind, initialSelection[kind] || {});
  const savedChoice = (kind, automaticLabel) => {
    const value = initialChoice(kind);
    const choices = [["auto", automaticLabel]];
    if (kind === "subtitles") choices.push(["off", vt("channel.trackSubtitleOff")]);
    if (value !== "auto" && value !== "off" && value !== "custom") choices.push([value, vt("channel.trackSaved")]);
    choices.push(["custom", vt("channel.trackCustom")]);
    return choices;
  };
  const videoSelect = select("video_selection", savedChoice("video", vt("channel.trackVideoAuto")), initialChoice("video"));
  const audioSelect = select("audio_selection", savedChoice("audio", vt("channel.trackAudioAuto")), initialChoice("audio"));
  const subtitleSelect = select("subtitle_selection", savedChoice("subtitles", vt("channel.trackSubtitleAuto")), initialChoice("subtitles"));
  const videoCustom = input("video_representation_id", initialSelection.video?.track?.representation_id || "", { placeholder: "Representation ID" });
  const audioCustom = input("audio_representation_id", initialSelection.audio?.track?.representation_id || "", { placeholder: "Representation ID" });
  const subtitleCustom = input("subtitle_representation_id", initialSelection.subtitles?.track?.representation_id || "", { placeholder: "Representation ID" });
  const customFields = [
    [videoSelect, videoCustom],
    [audioSelect, audioCustom],
    [subtitleSelect, subtitleCustom],
  ];
  const syncCustomFields = () => {
    for (const [selection, custom] of customFields) custom.hidden = selection.value !== "custom";
  };
  customFields.forEach(([selection]) => selection.addEventListener("change", syncCustomFields));
  syncCustomFields();

  let inspectedTracks = new Map();
  let probeController = null;
  let probeReady = false;
  let probeInvalidated = false;
  let inspectedNativeSupported = null;
  const probeBadge = badge(vt("channel.trackIdle"), "neutral");
  const probeSummary = h("strong", { text: vt("channel.trackPrompt") });
  const probeDetail = h("p", { class: "field-hint", text: vt("channel.trackPromptHint") });
  const probeButton = button(vt("channel.trackInspect"), { kind: "primary", iconName: "activity" });

  const selectionFor = (kind, control, custom) => {
    const value = control.value;
    if (value === "auto" || value === "off") return { mode: value };
    if (value === "custom") {
      const fallbackMode = kind === "video" ? "exact" : "prefer";
      return { mode: fallbackMode, track: customSelector(custom.value) };
    }
    const [mode, key] = value.split("|");
    const track = inspectedTracks.get(key);
    return { mode, track: track ? selectorForTrack(track) : { key } };
  };
  const currentSelection = () => ({
    video: selectionFor("video", videoSelect, videoCustom),
    audio: {
      ...selectionFor("audio", audioSelect, audioCustom),
      preferred_languages: preferredAudioInput.value.split(",").map((value) => value.trim()).filter(Boolean),
    },
    subtitles: selectionFor("subtitles", subtitleSelect, subtitleCustom),
  });

  const validateCustomSelection = () => {
    const emptyCustom = customFields.find(([selection, custom]) => selection.value === "custom" && !custom.value.trim());
    if (!emptyCustom) return true;
    emptyCustom[1].hidden = false;
    emptyCustom[1].setAttribute("aria-invalid", "true");
    emptyCustom[1].focus();
    toast(vt("channel.trackCustomRequired"), vt("channel.trackCustomRequiredHint"), "danger");
    return false;
  };

  const validateEngineSelection = () => {
    const subtitleMode = currentSelection().subtitles.mode;
    const explicitSubtitle = subtitleMode === "prefer" || subtitleMode === "only";
    const compatibilityRequired = packagerSelect.value === "ffmpeg" || (probeReady && inspectedNativeSupported === false);
    if (explicitSubtitle && compatibilityRequired) {
      subtitleSelect.setAttribute("aria-invalid", "true");
      subtitleSelect.focus();
      toast(vt("channel.trackCompatibilitySubtitle"), vt("channel.trackCompatibilitySubtitleHint"), "danger");
      return false;
    }
    const scheme = proxyKind(selectedProxyURL()).toLowerCase();
    if (ingressSelect.value === "dash" && compatibilityRequired && scheme.startsWith("socks")) {
      egressSelect.setAttribute("aria-invalid", "true");
      egressSelect.focus();
      toast(vt("channel.egressFFmpegSOCKS"), vt("channel.egressFFmpegSOCKSHint"), "danger");
      return false;
    }
    return true;
  };

  const validateFreshInspection = () => {
    const selection = currentSelection();
    const explicit = selection.video.mode !== "auto" || selection.audio.mode !== "auto" ||
      (selection.subtitles.mode !== "auto" && selection.subtitles.mode !== "off");
    if (ingressSelect.value !== "dash" || !explicit || !probeInvalidated) return true;
    probeButton.focus();
    toast(vt("channel.trackInspectionRequired"), vt("channel.trackInspectionRequiredHint"), "danger");
    return false;
  };

  const drawInspection = (inspection) => {
    const labelCopy = localizedTrackCopy();
    inspectedTracks = new Map(
      [...(inspection.videos || []), ...(inspection.audios || []), ...(inspection.subtitles || [])].map((track) => [track.key, track]),
    );
    const recommended = inspection.recommendation || {};
    replaceTrackChoices(videoSelect, [
      ["auto", vt("channel.trackVideoAuto")],
      ...(inspection.videos || []).map((track) => [`cap|${track.key}`, `${track.key === recommended.video_key ? "★ " : ""}${videoTrackLabel(track, labelCopy)}`, track.ambiguous]),
      ...(inspection.videos || []).map((track) => [`exact|${track.key}`, `${vt("channel.trackExactPrefix")} ${videoTrackLabel(track, labelCopy)}`, track.ambiguous]),
      ["custom", vt("channel.trackCustom")],
    ]);
    replaceTrackChoices(audioSelect, [
      ["auto", vt("channel.trackAudioAuto")],
      ...(inspection.audios || []).map((track) => [`prefer|${track.key}`, `${track.key === recommended.audio_key ? "★ " : ""}${audioTrackLabel(track, labelCopy)}`, track.ambiguous]),
      ...(inspection.audios || []).map((track) => [`only|${track.key}`, `${vt("channel.trackOnlyPrefix")} ${audioTrackLabel(track, labelCopy)}`, track.ambiguous]),
      ["custom", vt("channel.trackCustom")],
    ]);
    replaceTrackChoices(subtitleSelect, [
      ["auto", vt("channel.trackSubtitleAuto")],
      ["off", vt("channel.trackSubtitleOff")],
      ...(inspection.subtitles || []).map((track) => [`prefer|${track.key}`, `${track.key === recommended.subtitle_key ? "★ " : ""}${subtitleTrackLabel(track, labelCopy)}`, track.ambiguous || !track.native_supported || !inspection.native_supported]),
      ...(inspection.subtitles || []).map((track) => [`only|${track.key}`, `${vt("channel.trackOnlyPrefix")} ${subtitleTrackLabel(track, labelCopy)}`, track.ambiguous || !track.native_supported || !inspection.native_supported]),
      ["custom", vt("channel.trackCustom"), !inspection.native_supported],
    ]);
    syncCustomFields();
    probeReady = true;
    probeInvalidated = false;
    inspectedNativeSupported = Boolean(inspection.native_supported);
    probeBadge.className = `badge badge-${inspection.native_supported ? "success" : "warning"}`;
    probeBadge.replaceChildren(h("span", { text: inspection.native_supported ? vt("channel.trackNative") : vt("channel.trackCompatibility") }));
    probeSummary.textContent = trackSummary(inspection, labelCopy);
    const keyStatus = inspection.key_status === "matched"
      ? vt("channel.trackKeysMatched")
      : inspection.key_status === "missing"
        ? vt("channel.trackKeysMissing", { count: inspection.missing_key_kids?.length || 0 })
        : inspection.key_status === "unknown" ? vt("channel.trackKeysUnknown") : vt("channel.trackClear");
    const limitation = inspection.native_supported ? "" : vt("channel.trackCompatibilityHint");
    probeDetail.textContent = [inspection.compatibility_reason, limitation, keyStatus].filter(Boolean).join(" · ");
  };

  const markProbeStale = () => {
    const wasLoading = probeButton.disabled;
    const hadResult = probeReady || wasLoading;
    probeController?.abort();
    probeReady = false;
    probeInvalidated = true;
    inspectedNativeSupported = null;
    if (!hadResult) return;
    probeBadge.className = "badge badge-warning";
    probeBadge.replaceChildren(h("span", { text: vt("channel.trackStale") }));
    probeDetail.textContent = vt("channel.trackStaleHint");
  };
  [
    idInput, sourceInput, ingressSelect, userAgentInput, headersInput, packagerSelect,
    egressSelect, quickProxyName, quickProxyURL,
    videoSelect, audioSelect, subtitleSelect, videoCustom, audioCustom, subtitleCustom,
  ].forEach((control) => control.addEventListener("input", markProbeStale));

  const packagerField = field(vt("channel.packager"), packagerSelect, vt("channel.packagerHint"));
  const syncIngress = () => {
    const isDash = ingressSelect.value === "dash";
    packagerField.hidden = !isDash;
    restartSelect.disabled = isDash;
    if (isDash) restartSelect.value = "true";
    sourceInput.placeholder = isDash ? "https://example.com/live/channel/manifest.mpd" : "https://example.com/live/channel/index.m3u8";
    const label = probeButton.querySelector("span");
    if (label) label.textContent = vt(isDash ? "channel.trackInspect" : "channel.sourceTestDraft");
  };
  ingressSelect.addEventListener("change", syncIngress);
  syncIngress();

  const dirtyLabel = h("span", { class: "muted", text: vt("channel.noChanges") });
  const saveButton = button(vt(isNew ? "channel.create" : "channel.save"), { kind: "primary", type: "submit", iconName: "check" });

  const onEdit = () => {
    ctx.markDirty(true);
    dirtyLabel.textContent = vt("channel.unsaved");
    dirtyLabel.classList.add("is-dirty");
  };
  form.addEventListener("input", onEdit);
  form.addEventListener("change", onEdit);

  const buildDraft = () => {
    const strategyValue = strategySelect.value;
    const ingress = ingressSelect.value;
    const headers = parseHeaderLines(headersInput.value);
    const source = resolveSourceFields(sourceInput.value, store.upstreams);
    return {
      id: idInput.value.trim() || channel.id,
      title: titleInput.value.trim(),
      group: groupInput.value.trim(),
      logo_url: logoInput.value.trim(),
      epg_id: epgIDInput.value.trim(),
      epg_name: epgNameInput.value.trim(),
      epg_source: epgSourceSelect.value,
      source_url: source.sourceURL,
      upstream: source.upstream,
      path: source.path,
      ingress,
      disabled: Boolean(channel.disabled),
      on_demand: strategyValue !== "persistent",
      autostart: strategyValue !== "ondemand",
      idle_timeout_sec: Number(idleInput.value || 90),
      max_viewers: Number(maxViewersInput.value || 0),
      prefer_height: Number(heightInput.value || 0),
      preferred_audio_languages: preferredAudioInput.value.split(",").map((value) => value.trim()).filter(Boolean),
      selection: currentSelection(),
      packager: ingress === "dash" ? packagerSelect.value : "",
      user_agent: userAgentInput.value.trim(),
      headers,
      restart_on_failure: ingress === "dash" || restartSelect.value === "true",
      egress: currentEgress(),
    };
  };

  probeButton.addEventListener("click", async () => {
    if (!isValidSourceURL(sourceInput.value)) {
      sourceInput.setAttribute("aria-invalid", "true");
      sourceInput.focus();
      toast(vt("channel.invalidSource"), vt("channel.invalidSourceHint"), "danger");
      return;
    }
    if (!validateCustomSelection() || !validateEngineSelection()) return;
    probeController?.abort();
    probeController = new AbortController();
    probeButton.disabled = true;
    probeBadge.className = "badge badge-info";
    probeBadge.replaceChildren(h("span", { text: vt("channel.trackInspecting") }));
    probeSummary.textContent = vt("channel.trackReading");
    probeDetail.textContent = vt("channel.trackReadingHint");
    try {
      const result = await endpoints.probeSource(buildDraft(), probeController.signal);
      if (result.inspection) {
        drawInspection(result.inspection);
      } else {
        probeReady = true;
        probeInvalidated = false;
        probeBadge.className = "badge badge-success";
        probeBadge.replaceChildren(h("span", { text: vt("channel.sourceAvailable") }));
        probeSummary.textContent = vt("channel.sourceConnectedVia", { via: result.proxy_id || "direct" });
        probeDetail.textContent = `HTTP ${result.status} · ${result.dur_ms} ms${result.final_url ? ` · ${result.final_url}` : ""}`;
      }
    } catch (error) {
      if (error?.name === "AbortError") return;
      probeReady = false;
      probeBadge.className = "badge badge-danger";
      probeBadge.replaceChildren(h("span", { text: vt("channel.trackFailed") }));
      probeSummary.textContent = vt("channel.trackUnreadable");
      probeDetail.textContent = error.message || vt("channel.noValidResponse");
    } finally {
      probeButton.disabled = false;
    }
  });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const body = buildDraft();

    const required = [
      [idInput, body.id],
      [titleInput, body.title],
      [sourceInput, sourceInput.value.trim()],
    ];
    for (const [control] of required) control.removeAttribute("aria-invalid");
    const missing = required.filter(([, value]) => !value);
    if (missing.length) {
      for (const [control] of missing) control.setAttribute("aria-invalid", "true");
      missing[0][0].focus();
      toast(vt("channel.required"), vt("channel.requiredHint"), "danger");
      return;
    }
    if (isNew && !/^[\p{L}\p{N}][\p{L}\p{N}._-]{0,63}$/u.test(body.id)) {
      idInput.setAttribute("aria-invalid", "true");
      idInput.focus();
      toast(vt("channel.invalidID"), vt("channel.invalidIDHint"), "danger");
      return;
    }
    if (!isValidSourceURL(sourceInput.value)) {
      sourceInput.setAttribute("aria-invalid", "true");
      sourceInput.focus();
      toast(vt("channel.invalidSource"), vt("channel.invalidSourceHint"), "danger");
      return;
    }
    if (body.egress?.new_proxy && !body.egress.new_proxy.url) {
      quickProxyURL.setAttribute("aria-invalid", "true");
      quickProxyURL.focus();
      toast(vt("channel.egressURLRequired"), vt("channel.egressURLRequiredHint"), "danger");
      return;
    }
    if (!validateCustomSelection() || !validateFreshInspection() || !validateEngineSelection()) return;

    saveButton.disabled = true;
    try {
      if (isNew) await endpoints.createChannel(body);
      else await endpoints.updateChannel(channel.id, body, revision);
      ctx.markDirty(false);
      invalidateCatalog();
      toast(vt(isNew ? "channel.created" : "channel.saved"));
      await ctx.navigate(`/admin/channels/${encodeURIComponent(body.id)}`);
      if (!isNew) await ctx.reload();
    } catch (error) {
      toastError(error, vt("channel.saveFailed"));
      saveButton.disabled = false;
    }
  });

  const dormant = !isNew && channel.disabled && !epg.match;

  const applyCandidate = (candidate) => {
    epgIDInput.value = candidate.channel_id;
    epgNameInput.value = candidate.name || "";
    epgSourceSelect.value = hasSourceOption(epgSourceSelect, candidate.source_id) ? candidate.source_id : "";
    onEdit();
    toast(vt("channel.epgFilled"), vt("channel.epgFilledDetail", { source: candidate.source_id, channel: candidate.channel_id }));
  };

  const applyLogo = (logo) => {
    logoInput.value = logo.url;
    onEdit();
    toast(vt("channel.logoFilled"), logo.source_id);
  };

  form.append(
    formSection("1", vt("channel.info"), vt("channel.infoDesc"),
      h("div", { class: "form-grid" },
        field(vt("channel.id"), idInput, vt(isNew ? "channel.idNewHint" : "channel.idHint")),
        field(vt("channel.name"), titleInput),
        field(vt("channel.group"), groupInput),
      ),
    ),
    formSection("2", vt("channel.source"), vt("channel.sourceDesc"),
      h("div", { class: "form-grid" },
        h("div", { class: "span-all" },
          field(vt("channel.sourceURL"), sourceInput, vt("channel.sourceHint")),
          h("div", { class: "panel egress-picker" },
            h("div", { class: "form-grid" },
              field(vt("channel.egress"), egressSelect, vt("channel.egressHint")),
              h("div", { class: "field" },
                h("span", { class: "field-label", text: vt("channel.egressConnectivity") }),
                h("div", { class: "inline-row" }, egressTestButton),
                egressStatus,
              ),
              quickProxyPanel,
            ),
          ),
          h("div", { class: "track-discovery", role: "status", "aria-live": "polite" },
            h("div", { class: "track-discovery-head" },
              h("div", { class: "identity-copy" },
                h("div", { class: "inline-row" }, probeSummary, probeBadge),
                probeDetail,
              ),
              probeButton,
            ),
            h("div", { class: "form-grid track-grid" },
              field(vt("channel.trackVideo"), h("div", { class: "track-control" }, videoSelect, videoCustom), vt("channel.trackVideoHint")),
              field(vt("channel.trackAudio"), h("div", { class: "track-control" }, audioSelect, audioCustom), vt("channel.trackAudioHint")),
              field(vt("channel.trackSubtitle"), h("div", { class: "track-control" }, subtitleSelect, subtitleCustom), vt("channel.trackSubtitleHint")),
            ),
          ),
        ),
      ),
    ),
    formSection("3", vt("channel.runtime"), vt("channel.runtimeDesc"),
      h("div", { class: "form-grid" },
        field(vt("channel.mediaFormat"), ingressSelect),
        field(vt("channel.runtime"), strategySelect),
        field(vt("channel.idle"), idleInput, vt("channel.idleHint")),
        field(vt("channel.height"), heightInput, vt("channel.heightHint")),
        field(vt("channel.maxViewers"), maxViewersInput, vt("channel.maxViewersHint")),
        packagerField,
      ),
    ),
    formSection("4", vt("channel.epgSection"), vt("channel.epgDesc"),
      h("div", { class: "form-grid" },
        h("div", { class: "span-all" },
          h("div", { class: "inline-row" },
            dormant ? badge(vt("state.disabled"), "neutral") : matchBadge(epg.match?.status),
            h("span", { class: "muted", text: dormant ? vt("channel.dormantHint") : matchHint(epg.match) }),
            button(vt("channel.findEPG"), {
              size: "small",
              iconName: "search",
              onClick: () => openCandidateModal(epg.match, applyCandidate, dormant),
            }),
            button(vt("channel.chooseLogo"), {
              size: "small",
              iconName: "eye",
              onClick: () => openLogoModal(epg.match, applyLogo, dormant),
            }),
          ),
        ),
        field(vt("channel.epgID"), epgIDInput, vt("channel.epgIDHint")),
        field(vt("channel.epgName"), epgNameInput, vt("channel.epgNameHint")),
        field(vt("channel.epgSource"), epgSourceSelect, vt("channel.epgSourceHint")),
        field(vt("channel.logo"), logoInput, vt("channel.logoHint")),
      ),
    ),
    h("details", { class: "disclosure" },
      h("summary", {}, icon("sliders-horizontal", 18), h("span", { text: vt("channel.advanced") }), h("small", { text: vt("channel.advancedSummary") })),
      h("div", { class: "disclosure-body" },
        h("div", { class: "form-grid" },
          field("User-Agent", userAgentInput, vt("channel.userAgentHint")),
          field(vt("channel.recovery"), restartSelect, vt("channel.recoveryHint")),
          field(vt("channel.audioFallback"), preferredAudioInput, vt("channel.audioFallbackHint")),
          h("div", { class: "field span-all" },
            h("label", { class: "field-label", htmlFor: "channel-headers", text: vt("channel.headers") }),
            Object.assign(headersInput, { id: "channel-headers" }),
            h("p", { class: "field-hint", text: vt("channel.headersHint") }),
          ),
        ),
      ),
    ),
    h("div", { class: "form-footer" },
      dirtyLabel,
      h("div", { class: "form-footer-actions" }, linkButton(vt("common.cancel"), "/admin/channels"), saveButton),
    ),
  );

  return h("div", { class: "editor" }, buildCommandBar(ctx, channel, revision, isNew), card({ body: form, flush: true }), buildDangerZone(ctx, channel, revision, isNew));
}

function epgSourceChoices(sources, current) {
  const choices = [["", vt("channel.epgAny")], ...sources.map((source) => [source.id, `${source.id} · ${source.name}`])];
  if (current && !sources.some((source) => source.id === current)) choices.push([current, vt("channel.epgInvalid", { id: current })]);
  return choices;
}

function hasSourceOption(element, value) {
  return [...element.options].some((option) => option.value === value);
}

function matchHint(match) {
  if (!match) return vt("channel.noMatchYet");
  const count = (match.candidates || []).length;
  if (match.status === "matched") return vt("channel.matched", { source: match.match?.source_id || vt("channel.epgSourceGeneric"), channel: match.match?.channel_id || "" });
  if (match.status === "suggested") return vt("channel.suggested", { count });
  return vt("channel.noCandidateHint");
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
              candidate.names?.length > 1 ? h("small", { text: candidate.names.join(vt("common.listSeparator")) }) : null,
            ),
            button(vt("common.use"), {
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
    : emptyState(vt("channel.noCandidates"), dormant ? vt("channel.dormantHint") : vt("channel.noCandidatesHint"));

  openModal({
    title: vt("channel.findTitle"),
    description: vt("channel.findDesc"),
    body,
    actions: [button(vt("common.close"), { onClick: closeModal })],
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
            button(vt("common.use"), {
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
    : emptyState(vt("channel.noLogos"), dormant ? vt("channel.dormantHint") : vt("channel.noLogosHint"));

  openModal({
    title: vt("channel.logoTitle"),
    description: vt("channel.logoDesc"),
    body,
    actions: [button(vt("common.close"), { onClick: closeModal })],
  });
}

function buildCommandBar(ctx, channel, revision, isNew) {
  const statusSlot = h("span", { class: "state-slot" }, isNew ? badge(vt("channel.notCreated"), "neutral") : stateBadge(channel, sessionFor(channel.id)));
  const actionSlot = h("div", { class: "command-actions" });

  const paint = () => {
    if (!ctx.alive()) return;
    const session = sessionFor(channel.id);
    if (!isNew) statusSlot.replaceChildren(stateBadge(channel, session));
    actionSlot.replaceChildren(
      ...(isNew
        ? []
        : [
            button(vt("channel.probe"), { iconName: "activity", onClick: () => probe(channel.id) }),
            button(vt("channel.start"), { iconName: "power", disabled: channel.disabled, onClick: () => warmup(channel.id) }),
            button(vt("channel.openPreview"), { kind: "primary", iconName: "play", disabled: channel.disabled, onClick: () => previewChannel(channel) }),
            session ? iconButton("square", vt("channel.stopCurrent"), { kind: "danger", variant: "outline", onClick: () => stop(ctx, channel.id) }) : null,
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
        h("strong", { text: isNew ? vt("channels.add") : channel.title || channel.id }),
        h("small", { class: "mono", text: isNew ? vt("channel.createBelow") : channel.id }),
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
      toast(vt(next.disabled ? "state.disabled" : "common.enable"));
      await ctx.reload();
    } catch (error) {
      toastError(error, vt("channel.statusUpdatedFailed"));
    }
  };

  const remove = async () => {
    const accepted = await confirmDialog({
      title: vt("channel.deleteTitle"),
      description: vt("channel.deleteDesc"),
      warning: vt("channel.deleteWarning"),
      confirmLabel: vt("channel.deleteForever"),
      expect: channel.id,
    });
    if (!accepted) return;
    try {
      const detail = await endpoints.channel(channel.id);
      await endpoints.deleteChannel(channel.id, detail.revision);
      ctx.markDirty(false);
      invalidateCatalog();
      toast(vt("channel.deleted"));
      await ctx.navigate("/admin/channels");
    } catch (error) {
      toastError(error, vt("channel.deleteFailed"));
    }
  };

  return card({
    title: vt("channel.management"),
    tone: "danger",
    body: h(
      "div",
      { class: "list" },
      h(
        "div",
        { class: "list-item" },
        h("span", {}, h("strong", { text: vt(channel.disabled ? "channel.enableTitle" : "channel.disableTitle") }), h("small", { text: vt(channel.disabled ? "channel.enableHint" : "channel.disableHint") })),
        button(vt(channel.disabled ? "common.enable" : "common.disable"), { kind: channel.disabled ? "secondary" : "danger", size: "small", onClick: toggle }),
      ),
      h(
        "div",
        { class: "list-item" },
        h("span", {}, h("strong", { text: vt("channel.deleteRow") }), h("small", { text: vt("channel.deleteRowHint") })),
        button(vt("common.delete"), { kind: "danger", size: "small", onClick: remove }),
      ),
    ),
  });
}

function replaceTrackChoices(control, choices) {
  const current = control.value;
  if (current && !choices.some(([value]) => value === current)) {
    choices.splice(Math.max(choices.length - 1, 0), 0, [current, vt("channel.trackMissing")]);
  }
  control.replaceChildren(...choices.map(([value, label, disabled]) => h("option", { value, text: label, disabled })));
  if (current) control.value = current;
}

function proxyKind(raw) {
  try {
    const scheme = new URL(raw).protocol.replace(":", "").toUpperCase();
    return scheme || "PROXY";
  } catch {
    return "PROXY";
  }
}

function localizedTrackCopy() {
  return {
    video: vt("channel.trackVideo"),
    unknownLanguage: vt("channel.trackUnknownLanguage"),
    compatibility: vt("channel.trackCompatibilityShort"),
    ambiguous: vt("channel.trackAmbiguous"),
    channels: (count) => vt("channel.trackChannels", { count }),
    summary: (inspection) => vt("channel.trackSummary", {
      mode: inspection.dynamic ? "DASH LIVE" : "DASH VOD",
      videos: inspection.videos?.length || 0,
      audios: inspection.audios?.length || 0,
      subtitles: inspection.subtitles?.length || 0,
    }),
  };
}

function parseHeaderLines(value) {
  const headers = {};
  for (const line of value.split("\n")) {
    const split = line.indexOf(":");
    if (split > 0) headers[line.slice(0, split).trim()] = line.slice(split + 1).trim();
  }
  return headers;
}

async function probe(id) {
  toast(vt("channel.checking"), vt("channel.connecting"), "info");
  try {
    const result = await endpoints.probeChannel(id);
    if (result.ok) {
      const detail = result.inspection ? trackSummary(result.inspection, localizedTrackCopy()) : `HTTP ${result.status}`;
      toast(vt("channel.sourceOK"), `${detail} · ${result.dur_ms} ms`);
    }
    else toast(vt("channel.sourceNoResponse"), result.error || vt("channel.noValidResponse"), "danger");
  } catch (error) {
    toastError(error, vt("channel.checkFailed"));
  }
}

async function warmup(id) {
  try {
    await endpoints.warmupChannel(id);
    await refreshStatus();
    toast(vt("channel.starting"), vt("channel.startingHint"));
  } catch (error) {
    toastError(error, vt("channel.startFailed"));
  }
}

async function stop(ctx, id) {
  const accepted = await confirmDialog({
    title: vt("channel.stopTitle"),
    description: vt("channel.stopDesc", { id }),
    warning: vt("channel.stopWarning"),
    confirmLabel: vt("channel.stopAction"),
  });
  if (!accepted) return;
  try {
    await endpoints.stopSession(id);
    await refreshStatus();
    toast(vt("channel.stopped"));
  } catch (error) {
    toastError(error, vt("channel.stopFailed"));
  }
}
