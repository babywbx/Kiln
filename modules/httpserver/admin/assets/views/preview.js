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
        src: "/admin/assets/third_party/hls.light.min.js",
        onLoad: () => resolve(window.Hls),
        onError: () => reject(new Error("无法加载播放器")),
      });
      document.head.append(script);
    });
  }
  return hlsPromise;
}

export async function previewChannel(channel) {
  try {
    const preview = await endpoints.previewChannel(channel.id);
    const video = h("video", { class: "player", controls: true, autoplay: true, playsinline: true });
    const expiry = new Date(preview.expires_at).toLocaleTimeString("zh-Hans");

    let player = null;
    openModal({
      title: channel.title || channel.id,
      description: `播放预览 · 临时凭证将在 ${expiry} 过期`,
      body: video,
      actions: [button("关闭", { onClick: closeModal })],
      onClose: () => {
        player?.destroy();
        video.removeAttribute("src");
        video.load();
      },
    });

    if (video.canPlayType("application/vnd.apple.mpegurl")) {
      video.src = preview.play_url;
      return;
    }

    const Hls = await loadHls();
    if (!Hls?.isSupported()) throw new Error("当前浏览器不支持 HLS Media Source");
    player = new Hls({ enableWorker: true, lowLatencyMode: true });
    player.loadSource(preview.play_url);
    player.attachMedia(video);
  } catch (error) {
    toastError(error, "无法打开预览");
  }
}
