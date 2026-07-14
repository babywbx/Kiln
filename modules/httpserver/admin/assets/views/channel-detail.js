import { frag, h, icon } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { isValidSourceURL, resolveSourceFields } from "/admin/assets/core/source-url.js";
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
  keys: "", keys_file: "", user_agent: "", headers: {}, restart_on_failure: false, prefer_height: 0,
  packager: "", epg_id: "", epg_name: "", epg_source: "",
};

export async function renderChannelDetail(ctx) {
  const isNew = !ctx.id && ctx.query.get("new") === "1";
  const [, detail, matchData, sourceData] = await Promise.all([
    loadCatalog({ signal: ctx.signal }),
    isNew ? null : endpoints.channel(ctx.id, ctx.signal),
    endpoints.epgMatches(ctx.signal),
    endpoints.epgSources(ctx.signal),
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

  const editor = buildEditor(ctx, channel, revision, isNew, epg);
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

function buildEditor(ctx, channel, revision, isNew, epg) {
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

  const epgIDInput = input("epg_id", channel.epg_id, { placeholder: "456556" });
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
  const packagerSelect = select("packager", [
    ["", vt("channel.packagerGlobal")],
    ["auto", vt("channel.packagerAuto")],
    ["native", vt("channel.packagerNative")],
    ["ffmpeg", vt("channel.packagerFFmpeg")],
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
  const userAgentInput = input("user_agent", channel.user_agent, { placeholder: vt("channel.uaAuto", { version: store.version || "—" }) });
  const restartSelect = select("restart_on_failure", [["false", vt("channel.restartNo")], ["true", vt("channel.restartYes")]], String(Boolean(channel.restart_on_failure)));
  const headersInput = h("textarea", {
    name: "headers",
    rows: 4,
    placeholder: "Header-Name: value",
    value: Object.entries(channel.headers || {}).map(([key, value]) => `${key}: ${value}`).join("\n"),
  });

  const keysPanel = h("div", { class: "panel span-all" }, field(
    vt("channel.drmKeys"),
    keysInput,
    vt("channel.drmHint"),
  ));
  const packagerField = field(vt("channel.packager"), packagerSelect, vt("channel.packagerHint"));
  const syncIngress = () => {
    const isDash = ingressSelect.value === "dash";
    keysPanel.hidden = !isDash;
    packagerField.hidden = !isDash;
    restartSelect.disabled = isDash;
    if (isDash) restartSelect.value = "true";
    sourceInput.placeholder = isDash ? "https://example.com/live/channel/manifest.mpd" : "https://example.com/live/channel/index.m3u8";
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

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const strategyValue = strategySelect.value;
    const ingress = ingressSelect.value;

    const headers = {};
    for (const line of headersInput.value.split("\n")) {
      const split = line.indexOf(":");
      if (split > 0) headers[line.slice(0, split).trim()] = line.slice(split + 1).trim();
    }

    const source = resolveSourceFields(sourceInput.value, store.upstreams);
    const body = {
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
      [sourceInput, body.source_url],
      [keysInput, ingress !== "dash" || body.keys || body.keys_file],
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
    if (!isValidSourceURL(body.source_url)) {
      sourceInput.setAttribute("aria-invalid", "true");
      sourceInput.focus();
      toast(vt("channel.invalidSource"), vt("channel.invalidSourceHint"), "danger");
      return;
    }

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
        h("div", { class: "span-all" }, field(vt("channel.sourceURL"), sourceInput, vt("channel.sourceHint"))),
      ),
    ),
    formSection("3", vt("channel.runtime"), vt("channel.runtimeDesc"),
      h("div", { class: "form-grid" },
        field(vt("channel.mediaFormat"), ingressSelect),
        field(vt("channel.runtime"), strategySelect),
        field(vt("channel.idle"), idleInput, vt("channel.idleHint")),
        field(vt("channel.height"), heightInput, vt("channel.heightHint")),
        packagerField,
        keysPanel,
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

async function probe(id) {
  toast(vt("channel.checking"), vt("channel.connecting"), "info");
  try {
    const result = await endpoints.probeChannel(id);
    if (result.ok) toast(vt("channel.sourceOK"), `HTTP ${result.status} · ${result.dur_ms} ms`);
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
