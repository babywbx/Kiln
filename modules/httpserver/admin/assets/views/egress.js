import { frag, h } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { loadCatalog, store } from "/admin/assets/core/store.js";
import { badge, button, card, emptyState, field, input, notice, pageHead, select, table } from "/admin/assets/ui/kit.js";
import { closeModal, openModal, toast, toastError } from "/admin/assets/ui/overlay.js";

const POLICY_LABELS = {
  rewrite: "改写为 Kiln 播放地址",
  passthrough: "保留原始地址",
  auto: "自动选择",
};

const RULE_KINDS = {
  host_suffix: "主机名后缀",
  host_exact: "完整主机名",
  host_regex: "主机名正则表达式",
  channel_id: "频道标识符",
  url_regex: "完整地址正则表达式",
};

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
  const dirtyLabel = h("span", { class: "muted", text: "与已应用的配置一致" });

  const applyButton = button("应用更改", { kind: "primary", iconName: "check", disabled: true });
  const defaultSelect = h("select", { name: "default" });
  const policySelect = select("playlist_policy", Object.entries(POLICY_LABELS), draft.playlist_policy);
  const dockerInput = input("docker_proxy_host", draft.docker_proxy_host);

  // Any edit invalidates the last successful route test: applying an untested
  // draft is how you lock yourself out of every upstream at once.
  const touch = () => {
    tested = false;
    ctx.markDirty(true);
    applyButton.disabled = true;
    testResult.replaceChildren();
    dirtyLabel.textContent = "草稿未测试 · 请先执行一次成功的路由测试";
    dirtyLabel.classList.add("is-dirty");
  };

  const drawDefaults = () => {
    const options = [["direct", "direct · 直接连接"], ...draft.proxies.map((proxy) => [proxy.id, `${proxy.id} · ${proxy.name || "代理"}`])];
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
    touch();
  });
  dockerInput.addEventListener("input", () => {
    draft.docker_proxy_host = dockerInput.value.trim();
    touch();
  });

  const drawProxies = () => {
    if (!draft.proxies.length) {
      proxyBody.replaceChildren(emptyState("暂无代理", "所有流量将直接连接节目源。"));
      return;
    }
    proxyBody.replaceChildren(
      table(
        ["代理", "地址", "认证", "状态", ""],
        draft.proxies.map((proxy) =>
          h(
            "tr",
            {},
            h("td", {}, h("strong", { text: proxy.name || proxy.id }), h("div", { class: "mono muted", text: proxy.id })),
            h("td", { class: "mono truncate", text: safeHost(proxy.url) }),
            h("td", {}, hasCredentials(proxy) ? badge("已配置", "success") : badge("无凭据", "neutral")),
            h("td", {}, proxy.disabled ? badge("停用", "danger") : badge("启用", "success")),
            h(
              "td",
              {},
              h(
                "div",
                { class: "row-actions" },
                button(proxy.disabled ? "启用" : "停用", {
                  size: "small",
                  onClick: () => {
                    proxy.disabled = !proxy.disabled;
                    touch();
                    drawProxies();
                  },
                }),
                button("移除", {
                  kind: "danger",
                  size: "small",
                  onClick: () => {
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
      ruleBody.replaceChildren(emptyState("暂无路由规则", "默认出口会应用于所有未匹配的请求。"));
      return;
    }
    const sorted = [...draft.rules].sort((a, b) => (a.priority || 0) - (b.priority || 0));
    ruleBody.replaceChildren(
      table(
        ["规则", "优先级", "匹配", "出口", "状态", ""],
        sorted.map((rule) =>
          h(
            "tr",
            {},
            h("td", { class: "mono", text: rule.id }),
            h("td", { class: "mono", text: String(rule.priority ?? 0) }),
            h("td", {}, h("div", { class: "source-cell" }, h("span", { class: "truncate", text: RULE_KINDS[rule.kind] || rule.kind }), h("small", { class: "mono truncate", text: rule.pattern || "—" }))),
            h("td", { class: "mono", text: rule.proxy || rule.proxy_id || "direct" }),
            h("td", {}, rule.disabled ? badge("停用", "neutral") : badge("启用", "success")),
            h(
              "td",
              {},
              h(
                "div",
                { class: "row-actions" },
                button(rule.disabled ? "启用" : "停用", {
                  size: "small",
                  onClick: () => {
                    rule.disabled = !rule.disabled;
                    touch();
                    drawRules();
                  },
                }),
                button("移除", {
                  kind: "danger",
                  size: "small",
                  onClick: () => {
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

  const testURL = input("test_url", "", { type: "url", placeholder: "https://example.com/live/index.m3u8" });
  const testChannel = select("test_channel", [["", "不指定频道"], ...store.channels.map((channel) => [channel.id, channel.title || channel.id])], "");

  const testButton = button("测试路由", {
    iconName: "route",
    onClick: async () => {
      if (!testURL.value.trim()) {
        testURL.setAttribute("aria-invalid", "true");
        toast("请填写测试地址", "路由测试需要一个真实的上游地址。", "danger");
        return;
      }
      testURL.removeAttribute("aria-invalid");
      testButton.disabled = true;
      try {
        const result = await endpoints.testEgress({
          url: testURL.value.trim(),
          channel_id: testChannel.value,
          draft,
        });
        tested = Boolean(result.ok);
        applyButton.disabled = !tested;
        testResult.replaceChildren(
          result.ok
            ? notice(`连接成功 · 出口 ${result.via_proxy || "direct"} · HTTP ${result.status} · ${result.dur_ms} ms`, "success", "circle-check")
            : notice(`连接失败 · ${result.error || "未知错误"}`, "danger", "circle-alert"),
        );
        if (tested) dirtyLabel.textContent = "测试通过 · 可以应用";
      } catch (error) {
        toastError(error, "路由测试失败");
      } finally {
        testButton.disabled = false;
      }
    },
  });

  applyButton.addEventListener("click", async () => {
    if (!tested) return;
    applyButton.disabled = true;
    try {
      await endpoints.saveEgress(draft, revision);
      ctx.markDirty(false);
      toast("网络出口设置已应用");
      await ctx.reload();
    } catch (error) {
      toastError(error, "应用失败");
      applyButton.disabled = false;
    }
  });

  drawDefaults();
  drawProxies();
  drawRules();

  return frag(
    pageHead("网络出口", "设置节目源请求使用的默认连接、代理与匹配规则。", [applyButton]),
    h(
      "div",
      { class: "stack" },
      card({
        title: "默认连接",
        description: "未命中匹配规则的节目源请求使用这里的设置。",
        body: h(
          "div",
          { class: "form-grid" },
          field("默认出口", defaultSelect),
          field("播放列表策略", policySelect),
          field("容器代理主机", dockerInput, "仅用于容器内 FFmpeg 访问宿主机代理。"),
        ),
      }),
      h(
        "div",
        { class: "split-even" },
        card({
          title: "代理服务器",
          body: proxyBody,
          flush: true,
          action: button("添加代理", { size: "small", iconName: "plus", onClick: () => openProxyModal(draft, () => { touch(); drawDefaults(); drawProxies(); }) }),
        }),
        card({
          title: "路由规则",
          body: ruleBody,
          flush: true,
          action: button("添加规则", { size: "small", iconName: "plus", onClick: () => openRuleModal(draft, () => { touch(); drawRules(); }) }),
        }),
      ),
      card({
        title: "测试并应用",
        description: "先用当前草稿完成一次成功的连接测试，之后才能应用更改。",
        body: h(
          "div",
          { class: "stack" },
          h("div", { class: "form-grid" }, field("测试地址", testURL), field("频道（可选）", testChannel)),
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
    return "地址无效";
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

function openProxyModal(draft, after) {
  const idInput = input("id", "", { required: true, placeholder: "home-proxy" });
  const nameInput = input("name", "", { placeholder: "家庭出口" });
  const urlInput = input("url", "", { type: "url", required: true, placeholder: "socks5h://127.0.0.1:1080" });

  openModal({
    title: "添加代理",
    description: "凭据只写入服务端，应用后不会回显。",
    body: h(
      "div",
      { class: "stack" },
      field("代理标识符", idInput),
      field("显示名称", nameInput),
      field("代理地址", urlInput, "支持 http、https、socks5 与 socks5h。"),
    ),
    actions: [
      button("取消", { onClick: closeModal }),
      button("加入草稿", {
        kind: "primary",
        onClick: () => {
          const id = idInput.value.trim();
          const url = urlInput.value.trim();
          if (!id || !url) {
            toast("请填写代理标识符和地址", "", "danger");
            return;
          }
          if (draft.proxies.some((proxy) => proxy.id === id)) {
            toast("代理标识符已存在", "请换一个标识符，或先移除同名草稿项。", "danger");
            return;
          }
          draft.proxies.push({ id, name: nameInput.value.trim(), url, disabled: false });
          closeModal();
          after();
        },
      }),
    ],
  });
}

function openRuleModal(draft, after) {
  const idInput = input("id", "", { required: true, placeholder: "cn-upstream" });
  const priorityInput = input("priority", "100", { type: "number", min: 0 });
  const patternInput = input("pattern", "", { placeholder: "example.com" });
  const kindSelect = select("kind", Object.entries(RULE_KINDS), "host_suffix");
  const proxySelect = select("proxy", [["direct", "直接连接"], ...draft.proxies.map((proxy) => [proxy.id, proxy.name || proxy.id])], "direct");

  openModal({
    title: "添加路由规则",
    description: "规则只会加入草稿；请先测试再应用。",
    body: h(
      "div",
      { class: "form-grid" },
      field("规则标识符", idInput),
      field("优先级", priorityInput, "数字越小越优先。"),
      field("匹配类型", kindSelect),
      field("出口", proxySelect),
      h("div", { class: "span-all" }, field("匹配模式", patternInput)),
    ),
    actions: [
      button("取消", { onClick: closeModal }),
      button("加入草稿", {
        kind: "primary",
        onClick: () => {
          const id = idInput.value.trim();
          if (!id) {
            toast("请填写规则标识符", "", "danger");
            return;
          }
          if (draft.rules.some((rule) => rule.id === id)) {
            toast("规则标识符已存在", "请换一个标识符，或先移除同名草稿项。", "danger");
            return;
          }
          draft.rules.push({
            id,
            priority: Number(priorityInput.value || 100),
            kind: kindSelect.value,
            pattern: patternInput.value.trim(),
            proxy: proxySelect.value,
            disabled: false,
          });
          closeModal();
          after();
        },
      }),
    ],
  });
}
