function compact(value) {
  return String(value || "").trim();
}

export function formatBitrate(bitsPerSecond) {
  const value = Number(bitsPerSecond || 0);
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(value >= 10_000_000 ? 1 : 2)} Mbps`;
  if (value >= 1_000) return `${Math.round(value / 1_000)} kbps`;
  return value > 0 ? `${value} bps` : "";
}

export function videoTrackLabel(track, copy = {}) {
  const dimensions = track.width && track.height ? `${track.width}×${track.height}` : track.representation_id || copy.video || "Video";
  const fps = Number(track.frame_rate || 0) > 0 ? `${Number(track.frame_rate).toFixed(Number(track.frame_rate) % 1 ? 2 : 0)} fps` : "";
  return [dimensions, fps, compact(track.codec).toUpperCase(), formatBitrate(track.bandwidth), track.ambiguous ? copy.ambiguous || "轨道身份不唯一" : track.native_supported === false ? copy.compatibility || "兼容模式" : ""]
    .filter(Boolean)
    .join(" · ");
}

export function audioTrackLabel(track, copy = {}) {
  const language = compact(track.language) || copy.unknownLanguage || "未标注语言";
  const channels = Number(track.channels || 0) > 0 ? (copy.channels ? copy.channels(track.channels) : `${track.channels} 声道`) : "";
  const role = (track.roles || []).map(compact).filter(Boolean).join("/");
  return [language, role, channels, compact(track.codec).toUpperCase(), formatBitrate(track.bandwidth), track.ambiguous ? copy.ambiguous || "轨道身份不唯一" : track.native_supported === false ? copy.compatibility || "兼容模式" : ""]
    .filter(Boolean)
    .join(" · ");
}

export function subtitleTrackLabel(track, copy = {}) {
  const language = compact(track.language) || copy.unknownLanguage || "未标注语言";
  const role = (track.roles || []).map(compact).filter(Boolean).join("/");
  return [language, role, compact(track.codec).toUpperCase(), track.ambiguous ? copy.ambiguous || "轨道身份不唯一" : track.native_supported === false ? copy.compatibility || "兼容模式" : ""]
    .filter(Boolean)
    .join(" · ");
}

export function selectorForTrack(track) {
  return {
    key: compact(track.key),
    adaptation_set_id: compact(track.adaptation_set_id),
    representation_id: compact(track.representation_id),
    language: compact(track.language),
    role: compact(track.roles?.[0]),
    codec: compact(track.codec),
    height: Number(track.height || 0),
    frame_rate: compact(track.frame_rate_raw || track.frame_rate),
  };
}

export function customSelector(value) {
  return { representation_id: compact(value) };
}

export function choiceFromSelection(kind, selection = {}) {
  const mode = compact(selection.mode) || "auto";
  if (mode === "auto" || mode === "off") return mode;
  const key = compact(selection.track?.key);
  return key ? `${mode}|${key}` : "custom";
}

export function trackSummary(inspection, copy = {}) {
  if (copy.summary) return copy.summary(inspection);
  return `${inspection.dynamic ? "DASH LIVE" : "DASH VOD"} · ${inspection.videos?.length || 0} 画质 · ${inspection.audios?.length || 0} 音轨 · ${inspection.subtitles?.length || 0} 字幕`;
}
