import { h } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { button } from "/admin/assets/ui/kit.js";
import { closeModal, openModal, toastError } from "/admin/assets/ui/overlay.js";

let hlsPromise = null;

function loadHls() {
  if (window.Hls) return Promise.resolve(window.Hls);
  if (!hlsPromise) {
    hlsPromise = new Promise((resolve, reject) => {
      const script = h("script", {
        src: "/admin/assets/third_party/hls.min.js",
        onLoad: () => resolve(window.Hls),
        onError: () => reject(new Error("无法加载播放器")),
      });
      document.head.append(script);
    });
  }
  return hlsPromise;
}

function sameOrigin(playURL) {
  try {
    const parsed = new URL(playURL, window.location.href);
    return parsed.pathname + parsed.search;
  } catch {
    return playURL;
  }
}

export async function previewChannel(channel) {
  try {
    const preview = await endpoints.previewChannel(channel.id);
    const source = sameOrigin(preview.play_url);
    const video = h("video", { class: "player", controls: true, autoplay: true, playsinline: true });
    const status = h("p", { class: "muted", text: "正在连接…" });
    const expiry = new Date(preview.expires_at).toLocaleTimeString("zh-Hans");

    let player = null;
    openModal({
      title: channel.title || channel.id,
      description: `播放预览 · 临时凭证将在 ${expiry} 过期`,
      body: h("div", {}, video, status),
      actions: [button("关闭", { onClick: closeModal })],
      onClose: () => {
        player?.destroy();
        video.removeAttribute("src");
        video.load();
      },
    });

    const fail = (message) => {
      status.textContent = message;
      status.classList.add("is-error");
    };
    video.addEventListener("playing", () => {
      status.textContent = "";
      status.classList.remove("is-error");
    });

    if (video.canPlayType("application/vnd.apple.mpegurl")) {
      video.addEventListener("error", () => {
        fail(`播放失败：${video.error?.message || "浏览器拒绝了这个流"}`);
      });
      video.src = source;
      return;
    }

    const Hls = await loadHls();
    if (!Hls?.isSupported()) {
      fail("当前浏览器不支持 Media Source，无法预览。请改用 Safari。");
      return;
    }
    player = new Hls({ enableWorker: true, lowLatencyMode: true });
    player.on(Hls.Events.ERROR, (_event, data) => {
      if (!data.fatal) return;
      if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
        fail(`无法拉取播放列表：${data.details}`);
        player.startLoad();
        return;
      }
      if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
        fail(`解码失败：${data.details}。该编码可能不被此浏览器支持，可改用 Safari。`);
        player.recoverMediaError();
        return;
      }
      fail(`播放失败：${data.details}`);
      player.destroy();
    });
    player.loadSource(source);
    player.attachMedia(video);
  } catch (error) {
    toastError(error, "无法打开预览");
  }
}
