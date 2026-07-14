import { h } from "/admin/assets/core/dom.js";
import { endpoints } from "/admin/assets/core/api.js";
import { viewLocale, vt } from "/admin/assets/core/view-i18n.js";
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
        onError: () => reject(new Error(vt("preview.loadFailed"))),
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
    const status = h("p", { class: "muted", text: vt("preview.connecting") });
    const expiry = new Date(preview.expires_at).toLocaleTimeString(viewLocale(), { hour: "2-digit", minute: "2-digit" });

    let player = null;
    openModal({
      title: channel.title || channel.id,
      description: vt("preview.description", { time: expiry }),
      body: h("div", {}, video, status),
      actions: [button(vt("common.close"), { onClick: closeModal })],
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
        fail(vt("preview.failed", { detail: video.error?.message || vt("preview.browserRejected") }));
      });
      video.src = source;
      return;
    }

    const Hls = await loadHls();
    if (!Hls?.isSupported()) {
      fail(vt("preview.unsupported"));
      return;
    }
    player = new Hls({ enableWorker: true, lowLatencyMode: true });
    player.on(Hls.Events.ERROR, (_event, data) => {
      if (!data.fatal) return;
      if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
        fail(vt("preview.network", { detail: data.details }));
        player.startLoad();
        return;
      }
      if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
        fail(vt("preview.decode", { detail: data.details }));
        player.recoverMediaError();
        return;
      }
      fail(vt("preview.failed", { detail: data.details }));
      player.destroy();
    });
    player.loadSource(source);
    player.attachMedia(video);
  } catch (error) {
    toastError(error, vt("preview.openFailed"));
  }
}
