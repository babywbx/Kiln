import { frag, h } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { store } from "/admin/assets/core/store.js";
import { badge, button, card, field, input, pageHead } from "/admin/assets/ui/kit.js";
import { toast, toastError } from "/admin/assets/ui/overlay.js";

export async function renderSettings(ctx) {
  const data = await endpoints.settings(ctx.signal);
  if (!ctx.alive()) return frag();

  const baseInput = input("public_base_url", data.public_base_url || "", { type: "url", placeholder: "https://kiln.example.com" });
  const retentionInput = input("access_log_retention_days", data.access_log_retention_days || "30", { type: "number", min: 1, max: 3650 });

  const saveButton = button("保存设置", { kind: "primary", iconName: "check", disabled: true });
  const touch = () => {
    ctx.markDirty(true);
    saveButton.disabled = false;
  };
  baseInput.addEventListener("input", touch);
  retentionInput.addEventListener("input", touch);

  saveButton.addEventListener("click", async () => {
    saveButton.disabled = true;
    try {
      await endpoints.saveSettings(
        {
          public_base_url: baseInput.value.trim(),
          access_log_retention_days: String(retentionInput.value).trim(),
        },
        data.revision,
      );
      ctx.markDirty(false);
      toast("设置已保存");
      await ctx.reload();
    } catch (error) {
      toastError(error, "保存失败");
      saveButton.disabled = false;
    }
  });

  const runtime = h(
    "div",
    { class: "list" },
    runtimeRow("监听地址", data.listen || "—"),
    runtimeRow("播放鉴权", null, data.play_require_auth ? badge("已启用", "success") : badge("未启用", "warning")),
    runtimeRow("CORS 来源", (data.cors_origins || []).join("、") || "同源"),
    runtimeRow("公开主机", (data.public_hosts || []).join("、") || "未限制"),
    runtimeRow("服务版本", store.version || "—"),
  );

  return frag(
    pageHead("系统设置", "管理可在线更新的实例设置，并查看当前运行配置。", [saveButton]),
    h(
      "div",
      { class: "split" },
      card({
        title: "链接与日志",
        body: h(
          "div",
          { class: "stack" },
          field("公开访问地址", baseInput, "用于生成播放地址与播放列表地址；保存时会自动移除末尾斜杠。"),
          field("访问日志保留天数", retentionInput, "范围 1–3650 天。系统同时最多保留 5000 条访问记录。"),
        ),
      }),
      card({
        title: "当前运行配置",
        description: "这些项目需要修改服务器配置文件并重启后才能生效。",
        body: runtime,
      }),
    ),
  );
}

function runtimeRow(label, value, node = null) {
  return h(
    "div",
    { class: "list-item" },
    h("span", {}, h("strong", { text: label })),
    node || h("span", { class: "mono muted truncate", text: value }),
  );
}
