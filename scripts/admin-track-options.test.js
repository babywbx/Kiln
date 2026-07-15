import assert from "node:assert/strict";
import test from "node:test";

import {
  audioTrackLabel,
  choiceFromSelection,
  customSelector,
  selectorForTrack,
  trackSummary,
  videoTrackLabel,
} from "../modules/httpserver/admin/assets/core/track-options.js";

test("track labels expose resolution, frame rate, language, channels, and bitrate", () => {
  assert.equal(videoTrackLabel({ width: 3840, height: 2160, frame_rate: 50, codec: "hvc1", bandwidth: 15_200_000 }), "3840×2160 · 50 fps · HVC1 · 15.2 Mbps");
  assert.equal(audioTrackLabel({ language: "yue", roles: ["main"], channels: 6, codec: "mp4a.40.2", bandwidth: 192_000 }), "yue · main · 6 声道 · MP4A.40.2 · 192 kbps");
});

test("discovered and custom selections serialize without losing representation identity", () => {
  const track = { key: "trk_demo", adaptation_set_id: "video-main", representation_id: "v-1080p50", height: 1080, frame_rate_raw: "50" };
  assert.deepEqual(selectorForTrack(track), {
    key: "trk_demo",
    adaptation_set_id: "video-main",
    representation_id: "v-1080p50",
    language: "",
    role: "",
    codec: "",
    height: 1080,
    frame_rate: "50",
  });
  assert.deepEqual(customSelector(" custom-v "), { representation_id: "custom-v" });
  assert.equal(choiceFromSelection("video", { mode: "cap", track: { key: "trk_demo" } }), "cap|trk_demo");
});

test("inspection summary is compact and immediately scannable", () => {
  assert.equal(trackSummary({ dynamic: true, videos: [{}, {}], audios: [{}], subtitles: [{}, {}, {}] }), "DASH LIVE · 2 画质 · 1 音轨 · 3 字幕");
});
