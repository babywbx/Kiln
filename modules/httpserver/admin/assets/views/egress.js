import { frag, h, icon } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { egressOutcomeMessage, egressThroughputLabel } from "/admin/assets/core/egress-status.js";
import { i18n } from "/admin/assets/core/i18n.js";
import { loadCatalog, sourceURL, store } from "/admin/assets/core/store.js";
import { badge, button, card, emptyState, field, input, notice, pageHead, select, setBusy, table } from "/admin/assets/ui/kit.js";
import { closeModal, confirmDialog, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";

const POLICY_LABELS = {
  rewrite: "egress.policy.rewrite",
  passthrough: "egress.policy.passthrough",
  auto: "egress.policy.auto",
};

const POLICY_HINTS = {
  rewrite: "egress.policy.rewriteHint",
  passthrough: "egress.policy.passthroughHint",
  auto: "egress.policy.autoHint",
};

const RULE_KINDS = {
  host_suffix: "egress.ruleKind.hostSuffix",
  host_exact: "egress.ruleKind.hostExact",
  host_regex: "egress.ruleKind.hostRegex",
  channel_id: "egress.ruleKind.channelId",
  url_regex: "egress.ruleKind.urlRegex",
};

function translatedChoices(labels) {
  return Object.entries(labels).map(([value, key]) => [value, i18n.t(key)]);
}

export async function renderEgress(ctx) {
  await loadCatalog({ signal: ctx.signal });
  const remote = await endpoints.egress(ctx.signal);
  if (!ctx.alive()) return frag();

  const draft = {
    default: remote.default || "direct",
    playlist_policy: remote.playlist_policy || "rewrite",
    docker_proxy_host: remote.docker_proxy_host || "host.docker.internal",
    proxies: [...(remote.proxies || [])],
    rules: [...(remote.rules || [])],
  };
  const revision = remote.revision || 0;
  let tested = false;

  const proxyBody = h("div", {});
  const ruleBody = h("div", {});
  const testResult = h("div", {});
  const dirtyLabel = h("span", { class: "muted", text: i18n.t("egress.appliedState") });

  const applyButton = button(i18n.t("egress.apply"), { kind: "primary", iconName: "check", disabled: true });
  const defaultSelect = h("select", { name: "default" });
  const policySelect = select("playlist_policy", translatedChoices(POLICY_LABELS), draft.playlist_policy);
  const dockerInput = input("docker_proxy_host", draft.docker_proxy_host);
  const policyHint = h("p", { class: "field-hint", text: i18n.t(POLICY_HINTS[draft.playlist_policy]) });

  const touch = () => {
    tested = false;
    ctx.markDirty(true);
    applyButton.disabled = true;
    testResult.replaceChildren();
    dirtyLabel.textContent = i18n.t("egress.untestedDraft");
    dirtyLabel.classList.add("is-dirty");
  };

  const drawDefaults = () => {
    const options = [["direct", i18n.t("egress.directNoProxy")], ...draft.proxies.filter((proxy) => !proxy.disabled).map((proxy) => [proxy.id, `${proxy.id} · ${proxy.name || i18n.t("egress.proxy.generic")}`])];
    defaultSelect.replaceChildren(
      ...options.map(([value, label]) => h("option", { value, selected: value === draft.default, text: label })),
    );
  };

  defaultSelect.addEventListener("change", () => {
    draft.default = defaultSelect.value;
    touch();
  });
  policySelect.addEventListener("change", () => {
    draft.playlist_policy = policySelect.value;
    policyHint.textContent = i18n.t(POLICY_HINTS[draft.playlist_policy]);
    touch();
  });
  dockerInput.addEventListener("input", () => {
    draft.docker_proxy_host = dockerInput.value.trim();
    touch();
  });

  const drawProxies = () => {
    if (!draft.proxies.length) {
      proxyBody.replaceChildren(emptyState(i18n.t("egress.proxy.empty"), i18n.t("egress.proxy.emptyDescription")));
      return;
    }
    proxyBody.replaceChildren(
      table(
        [i18n.t("egress.proxy.table.proxy"), i18n.t("egress.proxy.table.address"), i18n.t("egress.proxy.table.auth"), i18n.t("egress.proxy.table.status"), ""],
        draft.proxies.map((proxy) =>
          h(
            "tr",
            {},
            h("td", {}, h("strong", { text: proxy.name || proxy.id }), h("div", { class: "mono muted", text: proxy.id })),
            h("td", { class: "mono truncate", text: safeHost(proxy.url) }),
            h("td", {}, hasCredentials(proxy) ? badge(i18n.t("egress.proxy.credentialsConfigured"), "success") : badge(i18n.t("egress.proxy.noCredentials"), "neutral")),
            h("td", {}, proxy.disabled ? badge(i18n.t("shared.disabled"), "danger") : badge(i18n.t("shared.enabled"), "success")),
            h(
              "td",
              {},
              h(
                "div",
                { class: "row-actions" },
                button(i18n.t(proxy.disabled ? "egress.enable" : "egress.disable"), {
                  size: "small",
                  onClick: () => {
                    proxy.disabled = !proxy.disabled;
                    if (proxy.disabled && draft.default === proxy.id) draft.default = "direct";
                    if (proxy.disabled) {
                      for (const rule of draft.rules) {
                        if ((rule.proxy || rule.proxy_id) === proxy.id) rule.disabled = true;
                      }
                    }
                    touch();
                    drawDefaults();
                    drawProxies();
                    drawRules();
                  },
                }),
                button(i18n.t("egress.edit"), {
                  size: "small",
                  onClick: () => openProxyModal(draft, () => { touch(); drawDefaults(); drawProxies(); }, proxy),
                }),
                button(i18n.t("egress.test"), { size: "small", onClick: (event) => testProxy(proxy, event.currentTarget) }),
                button(i18n.t("egress.remove"), {
                  kind: "danger",
                  size: "small",
                  onClick: async () => {
                    const linked = draft.rules.filter((rule) => (rule.proxy || rule.proxy_id) === proxy.id).length;
                    const accepted = await confirmDialog({
                      title: i18n.t("egress.proxy.removeTitle"),
                      description: i18n.t("egress.proxy.removeDescription", { name: proxy.name || proxy.id }),
                      warning: linked ? i18n.t("egress.proxy.removeWarning", { count: linked }) : "",
                      confirmLabel: i18n.t("egress.remove"),
                    });
                    if (!accepted) return;
                    draft.proxies = draft.proxies.filter((item) => item.id !== proxy.id);
                    draft.rules = draft.rules.filter((rule) => (rule.proxy || rule.proxy_id) !== proxy.id);
                    if (draft.default === proxy.id) draft.default = "direct";
                    touch();
                    drawDefaults();
                    drawProxies();
                    drawRules();
                  },
                }),
              ),
            ),
          ),
        ),
      ),
    );
  };

  const drawRules = () => {
    if (!draft.rules.length) {
      ruleBody.replaceChildren(emptyState(i18n.t("egress.rule.empty"), i18n.t("egress.rule.emptyDescription")));
      return;
    }
    const sorted = [...draft.rules].sort((a, b) => (a.priority || 0) - (b.priority || 0));
    ruleBody.replaceChildren(
      table(
        [i18n.t("egress.rule.table.rule"), i18n.t("egress.rule.table.priority"), i18n.t("egress.rule.table.match"), i18n.t("egress.rule.table.egress"), i18n.t("egress.rule.table.status"), ""],
        sorted.map((rule) =>
          h(
            "tr",
            {},
            h("td", { class: "mono", text: rule.id }),
            h("td", { class: "mono", text: String(rule.priority ?? 0) }),
            h("td", {}, h("div", { class: "source-cell" }, h("span", { class: "truncate", text: RULE_KINDS[rule.kind] ? i18n.t(RULE_KINDS[rule.kind]) : rule.kind }), h("small", { class: "mono truncate", text: rule.pattern || "—" }))),
            h("td", { class: "mono", text: rule.proxy || rule.proxy_id || "direct" }),
            h("td", {}, rule.disabled ? badge(i18n.t("shared.disabled"), "neutral") : badge(i18n.t("shared.enabled"), "success")),
            h(
              "td",
              {},
              h(
                "div",
                { class: "row-actions" },
                button(i18n.t(rule.disabled ? "egress.enable" : "egress.disable"), {
                  size: "small",
                  onClick: () => {
                    const proxyID = rule.proxy || rule.proxy_id;
                    const proxy = draft.proxies.find((item) => item.id === proxyID);
                    if (rule.disabled && proxy?.disabled) {
                      toast(i18n.t("egress.rule.cannotEnable"), i18n.t("egress.rule.proxyDisabled", { proxy: proxy.name || proxy.id }), "danger");
                      return;
                    }
                    rule.disabled = !rule.disabled;
                    touch();
                    drawRules();
                  },
                }),
                button(i18n.t("egress.edit"), { size: "small", onClick: () => openRuleModal(draft, () => { touch(); drawRules(); }, rule) }),
                button(i18n.t("egress.remove"), {
                  kind: "danger",
                  size: "small",
                  onClick: async () => {
                    const accepted = await confirmDialog({
                      title: i18n.t("egress.rule.removeTitle"),
                      description: i18n.t("egress.rule.removeDescription", { id: rule.id }),
                      confirmLabel: i18n.t("egress.remove"),
                    });
                    if (!accepted) return;
                    draft.rules = draft.rules.filter((item) => item.id !== rule.id);
                    touch();
                    drawRules();
                  },
                }),
              ),
            ),
          ),
        ),
      ),
    );
  };

  const testTarget = select("test_target", [
    ["bing", i18n.t("egress.test.targetBing")],
    ["source", i18n.t("egress.test.targetSource")],
  ], "bing");
  const testURL = input("test_url", "", { type: "url", placeholder: "https://example.com/live/index.m3u8" });
  const testChannel = select("test_channel", [["", i18n.t("egress.test.noChannel")], ...store.channels.map((channel) => [channel.id, channel.title || channel.id])], "");
  const testTargetField = field(i18n.t("egress.test.target"), testTarget);
  const testURLField = field(i18n.t("egress.test.url"), testURL);
  const testChannelField = field(i18n.t("egress.test.channel"), testChannel);
  const syncTestTarget = () => {
    const source = testTarget.value === "source";
    testURLField.hidden = !source;
    testChannelField.hidden = !source;
  };
  testTarget.addEventListener("change", syncTestTarget);
  syncTestTarget();
  testChannel.addEventListener("change", () => {
    const channel = store.channels.find((item) => item.id === testChannel.value);
    if (!channel) return;
    testURL.value = channel.source_url || sourceURL(channel.upstream, channel.path);
  });

  const testProxy = async (proxy, trigger) => {
    if (testTarget.value === "source" && !testURL.value.trim()) {
      testURL.setAttribute("aria-invalid", "true");
      testURL.focus();
      toast(i18n.t("egress.test.urlRequired"), i18n.t("egress.test.proxyURLRequiredDescription"), "danger");
      return;
    }
    const proxyDraft = { ...draft, default: proxy.id, rules: [], proxies: draft.proxies.map((item) => ({ ...item, disabled: item.id === proxy.id ? false : item.disabled })) };
    setBusy(trigger, true);
    try {
      const result = await endpoints.testEgress({ target: testTarget.value, url: testTarget.value === "source" ? testURL.value.trim() : "", channel_id: "", draft: proxyDraft });
      testResult.replaceChildren(resultNotice(result, proxy.id));
    } catch (error) {
      toastError(error, i18n.t("egress.test.proxyFailed"));
    } finally {
      setBusy(trigger, false);
    }
  };

  const testButton = button(i18n.t("egress.test.route"), {
    iconName: "route",
    onClick: async () => {
      if (testTarget.value === "source" && !testURL.value.trim()) {
        testURL.setAttribute("aria-invalid", "true");
        toast(i18n.t("egress.test.urlRequired"), i18n.t("egress.test.routeURLRequiredDescription"), "danger");
        return;
      }
      testURL.removeAttribute("aria-invalid");
      setBusy(testButton, true);
      try {
        const result = await endpoints.testEgress({
          target: testTarget.value,
          url: testTarget.value === "source" ? testURL.value.trim() : "",
          channel_id: testChannel.value,
          draft,
        });
        tested = Boolean(result.ok);
        applyButton.disabled = !tested;
        testResult.replaceChildren(resultNotice(result));
        if (tested) dirtyLabel.textContent = i18n.t("egress.test.passed");
      } catch (error) {
        toastError(error, i18n.t("egress.test.routeFailed"));
      } finally {
        setBusy(testButton, false);
      }
    },
  });

  applyButton.addEventListener("click", async () => {
    if (!tested) return;
    setBusy(applyButton, true);
    try {
      await endpoints.saveEgress(draft, revision);
      ctx.markDirty(false);
      toast(i18n.t("egress.applied"));
      await ctx.reload();
    } catch (error) {
      toastError(error, i18n.t("egress.applyFailed"));
      setBusy(applyButton, false);
    }
  });

  drawDefaults();
  drawProxies();
  drawRules();

  return frag(
    pageHead(i18n.t("egress.title"), i18n.t("egress.description"), [applyButton]),
    h(
      "div",
      { class: "stack" },
      card({
        title: i18n.t("egress.defaultTitle"),
        description: i18n.t("egress.defaultDescription"),
        body: h(
          "div",
          { class: "form-grid" },
          field(i18n.t("egress.defaultConnection"), defaultSelect, i18n.t("egress.defaultConnectionHint")),
          h("div", {}, field(i18n.t("egress.playlistPolicy"), policySelect), policyHint),
          field(i18n.t("egress.containerProxyHost"), dockerInput, i18n.t("egress.containerProxyHostHint")),
        ),
      }),
      h(
        "div",
        { class: "stack" },
        card({
          title: i18n.t("egress.proxy.title"),
          body: proxyBody,
          flush: true,
          action: h(
            "div",
            { class: "row-actions" },
            button(i18n.t("egress.transfer.import"), {
              size: "small",
              iconName: "file-up",
              onClick: () => openImportModal("proxies", (items) => {
                const summary = mergeProxies(draft, items);
                touch();
                drawDefaults();
                drawProxies();
                drawRules();
                return summary;
              }),
            }),
            button(i18n.t("egress.transfer.export"), { size: "small", iconName: "download", onClick: () => exportBundle("proxies", draft.proxies.map(exportableProxy)) }),
            button(i18n.t("egress.proxy.add"), { size: "small", iconName: "plus", onClick: () => openProxyModal(draft, () => { touch(); drawDefaults(); drawProxies(); }) }),
          ),
        }),
        card({
          title: i18n.t("egress.rule.title"),
          body: ruleBody,
          flush: true,
          action: h(
            "div",
            { class: "row-actions" },
            button(i18n.t("egress.transfer.import"), {
              size: "small",
              iconName: "file-up",
              onClick: () => openImportModal("rules", (items) => {
                const summary = mergeRules(draft, items);
                touch();
                drawRules();
                return summary;
              }),
            }),
            button(i18n.t("egress.transfer.export"), { size: "small", iconName: "download", onClick: () => exportBundle("rules", draft.rules.map(exportableRule)) }),
            button(i18n.t("egress.rule.add"), { size: "small", iconName: "plus", onClick: () => openRuleModal(draft, () => { touch(); drawRules(); }) }),
          ),
        }),
      ),
      card({
        title: i18n.t("egress.test.title"),
        description: i18n.t("egress.test.description"),
        body: h(
          "div",
          { class: "stack" },
          h("div", { class: "form-grid" }, testTargetField, testURLField, testChannelField),
          h("div", { class: "inline-row" }, testButton, dirtyLabel),
          testResult,
        ),
      }),
    ),
  );
}

function safeHost(raw) {
  try {
    const url = new URL(raw);
    return `${url.protocol}//${url.host}`;
  } catch {
    return i18n.t("egress.invalidAddress");
  }
}

function hasCredentials(proxy) {
  if (proxy.credential_configured) return true;
  try {
    return Boolean(new URL(proxy.url).username);
  } catch {
    return false;
  }
}

function openProxyModal(draft, after, existing = null) {
  const isEdit = Boolean(existing);
  const idInput = input("id", existing?.id || "", { required: true, disabled: isEdit, placeholder: "primary-proxy" });
  const nameInput = input("name", existing?.name || "", { placeholder: i18n.t("egress.proxy.namePlaceholder") });
  const urlInput = input("url", isEdit && existing?.credential_configured ? "" : existing?.url || "", { type: "url", required: !isEdit, placeholder: isEdit ? i18n.t("egress.proxy.urlEditPlaceholder") : "socks5h://127.0.0.1:1080" });

  openModal({
    title: isEdit ? i18n.t("egress.proxy.editTitle", { proxy: existing.name || existing.id }) : i18n.t("egress.proxy.addTitle"),
    description: i18n.t(isEdit ? "egress.proxy.editDescription" : "egress.proxy.addDescription"),
    body: h(
      "div",
      { class: "stack" },
      field(i18n.t("egress.proxy.id"), idInput),
      field(i18n.t("egress.proxy.name"), nameInput),
      field(i18n.t("egress.proxy.url"), urlInput, i18n.t("egress.proxy.urlHint")),
    ),
    actions: [
      button(i18n.t("shared.cancel"), { onClick: closeModal }),
      button(i18n.t(isEdit ? "egress.saveToDraft" : "egress.addToDraft"), {
        kind: "primary",
        onClick: () => {
          const id = idInput.value.trim();
          const url = urlInput.value.trim() || existing?.url || "";
          if (!id || !url) {
            toast(i18n.t("egress.proxy.required"), "", "danger");
            return;
          }
          if (!isEdit && draft.proxies.some((proxy) => proxy.id === id)) {
            toast(i18n.t("egress.proxy.idExists"), i18n.t("egress.proxy.idExistsDescription"), "danger");
            return;
          }
          const replacement = { ...existing, id, name: nameInput.value.trim(), url, disabled: Boolean(existing?.disabled) };
          if (isEdit) draft.proxies[draft.proxies.findIndex((proxy) => proxy.id === id)] = replacement;
          else draft.proxies.push(replacement);
          closeModal();
          after();
        },
      }),
    ],
  });
}

function openRuleModal(draft, after, existing = null) {
  const isEdit = Boolean(existing);
  const idInput = input("id", existing?.id || "", { required: true, disabled: isEdit, placeholder: "media-route" });
  const priorityInput = input("priority", existing?.priority ?? "100", { type: "number", min: 0 });
  const patternInput = input("pattern", existing?.pattern || "", { placeholder: "example.com" });
  const kindSelect = select("kind", translatedChoices(RULE_KINDS), existing?.kind || "host_suffix");
  const proxySelect = select("proxy", [["direct", i18n.t("egress.direct")], ...draft.proxies.map((proxy) => [proxy.id, proxy.name || proxy.id])], existing?.proxy || existing?.proxy_id || "direct");

  openModal({
    title: isEdit ? i18n.t("egress.rule.editTitle", { rule: existing.id }) : i18n.t("egress.rule.addTitle"),
    description: i18n.t("egress.rule.modalDescription"),
    body: h(
      "div",
      { class: "form-grid" },
      field(i18n.t("egress.rule.id"), idInput),
      field(i18n.t("egress.rule.priority"), priorityInput, i18n.t("egress.rule.priorityHint")),
      field(i18n.t("egress.rule.kind"), kindSelect),
      field(i18n.t("egress.rule.egress"), proxySelect),
      h("div", { class: "span-all" }, field(i18n.t("egress.rule.pattern"), patternInput)),
    ),
    actions: [
      button(i18n.t("shared.cancel"), { onClick: closeModal }),
      button(i18n.t(isEdit ? "egress.saveToDraft" : "egress.addToDraft"), {
        kind: "primary",
        onClick: () => {
          const id = idInput.value.trim();
          if (!id) {
            toast(i18n.t("egress.rule.idRequired"), "", "danger");
            return;
          }
          if (!isEdit && draft.rules.some((rule) => rule.id === id)) {
            toast(i18n.t("egress.rule.idExists"), i18n.t("egress.rule.idExistsDescription"), "danger");
            return;
          }
          const replacement = {
            ...existing,
            id,
            priority: Number(priorityInput.value || 100),
            kind: kindSelect.value,
            pattern: patternInput.value.trim(),
            proxy: proxySelect.value,
            disabled: Boolean(existing?.disabled),
          };
          if (isEdit) draft.rules[draft.rules.findIndex((rule) => rule.id === id)] = replacement;
          else draft.rules.push(replacement);
          closeModal();
          after();
        },
      }),
    ],
  });
}

function resultNotice(result, expectedProxy = "") {
  if (!result.ok) return notice(i18n.t("egress.result.failed", { error: egressOutcomeMessage(result) }), "danger", "circle-alert");
  const via = result.via_proxy || result.proxy_id || "direct";
  if (expectedProxy && via !== expectedProxy) {
    return notice(i18n.t("egress.result.wrongProxy", { expected: expectedProxy, actual: via }), "danger", "circle-alert");
  }
  const route = result.reason === "default" ? i18n.t("egress.result.defaultRoute") : i18n.t("egress.result.matchedRoute", { reason: result.reason || i18n.t("egress.result.defaultRoute") });
  const playlist = i18n.t(result.rewrite ? "egress.policy.rewrite" : "egress.policy.passthrough");
  const finalURL = result.final_url ? i18n.t("egress.result.finalURL", { url: result.final_url }) : "";
  const throughput = egressThroughputLabel(result);
  return notice(i18n.t("egress.result.success", { route, via, playlist, status: result.status, duration: result.dur_ms, finalURL }) + throughput, "success", "circle-check");
}

function exportableProxy(proxy) {
  return { id: proxy.id, name: proxy.name || "", url: proxy.url || "", disabled: Boolean(proxy.disabled) };
}

function exportableRule(rule) {
  return {
    id: rule.id,
    kind: rule.kind,
    pattern: rule.pattern || "",
    proxy: rule.proxy || rule.proxy_id || "direct",
    priority: Number(rule.priority) || 100,
    disabled: Boolean(rule.disabled),
  };
}

function exportBundle(kind, items) {
  if (!items.length) {
    toast(i18n.t("egress.transfer.empty"), i18n.t("egress.transfer.emptyHint"), "warning");
    return;
  }
  const payload = { kiln: `egress-${kind}`, version: 1, [kind]: items };
  const url = URL.createObjectURL(new Blob([`${JSON.stringify(payload, null, 2)}\n`], { type: "application/json" }));
  const anchor = h("a", { href: url, download: `kiln-egress-${kind}.json` });
  anchor.click();
  setTimeout(() => URL.revokeObjectURL(url), 0);
  toast(i18n.t("egress.transfer.exported", { count: items.length }));
}

function openImportModal(kind, apply) {
  let selectedFile = null;
  const filename = h("strong", { text: i18n.t("egress.transfer.drop") });
  const picker = h("input", { type: "file", accept: ".json,application/json", class: "sr-only" });
  const submit = button(i18n.t("egress.transfer.import"), {
    kind: "primary",
    iconName: "file-up",
    disabled: true,
    onClick: async () => {
      let items = null;
      try {
        const data = JSON.parse(await selectedFile.text());
        if (data?.kiln === `egress-${kind}` && Array.isArray(data[kind])) items = data[kind];
      } catch {
        items = null;
      }
      if (!items) {
        toast(i18n.t("egress.transfer.invalid"), i18n.t("egress.transfer.invalidHint"), "danger");
        return;
      }
      const summary = apply(items);
      closeModal();
      toast(i18n.t("egress.transfer.imported"), i18n.t("egress.transfer.importedDetail", summary), summary.added || summary.replaced ? "success" : "warning");
    },
  });
  const selectFile = (file) => {
    if (!file) return;
    selectedFile = file;
    filename.textContent = file.name;
    submit.disabled = false;
  };
  picker.addEventListener("change", () => selectFile(picker.files?.[0]));
  const dropzone = h(
    "label",
    {
      class: "file-dropzone",
      onDragOver: (event) => {
        event.preventDefault();
        dropzone.classList.add("is-dragging");
      },
      onDragLeave: () => dropzone.classList.remove("is-dragging"),
      onDrop: (event) => {
        event.preventDefault();
        dropzone.classList.remove("is-dragging");
        selectFile(event.dataTransfer?.files?.[0]);
      },
    },
    picker,
    icon("file-up", 28),
    h("span", { class: "file-dropzone-copy" }, filename, h("span", { text: i18n.t("egress.transfer.invalidHint") })),
  );
  openModal({
    title: i18n.t("egress.transfer.import"),
    body: dropzone,
    actions: [button(i18n.t("shared.cancel"), { onClick: closeModal }), submit],
  });
}

function mergeProxies(draft, items) {
  const summary = { added: 0, replaced: 0, skipped: 0 };
  for (const item of items) {
    const id = String(item?.id || "").trim();
    const url = String(item?.url || "").trim();
    if (!id || !url) {
      summary.skipped += 1;
      continue;
    }
    const entry = { id, name: String(item.name || ""), url, disabled: Boolean(item.disabled) };
    const at = draft.proxies.findIndex((proxy) => proxy.id === id);
    if (at >= 0) {
      draft.proxies[at] = { ...draft.proxies[at], ...entry };
      summary.replaced += 1;
    } else {
      draft.proxies.push(entry);
      summary.added += 1;
    }
  }
  return summary;
}

function mergeRules(draft, items) {
  const summary = { added: 0, replaced: 0, skipped: 0 };
  const known = new Set(["direct", "", ...draft.proxies.map((proxy) => proxy.id)]);
  for (const item of items) {
    const id = String(item?.id || "").trim();
    const pattern = String(item?.pattern || "").trim();
    const proxy = String(item?.proxy || item?.proxy_id || "direct").trim();
    if (!id || !pattern || !RULE_KINDS[item?.kind] || !known.has(proxy)) {
      summary.skipped += 1;
      continue;
    }
    const entry = { id, kind: item.kind, pattern, proxy, priority: Number(item.priority) || 100, disabled: Boolean(item.disabled) };
    const at = draft.rules.findIndex((rule) => rule.id === id);
    if (at >= 0) {
      draft.rules[at] = { ...draft.rules[at], ...entry };
      summary.replaced += 1;
    } else {
      draft.rules.push(entry);
      summary.added += 1;
    }
  }
  return summary;
}
