import { i18n } from "/admin/assets/core/i18n.js";

const OUTCOME_KEYS = {
  blocked: "egress.outcome.blocked",
  dns: "egress.outcome.dns",
  timeout: "egress.outcome.timeout",
  tls: "egress.outcome.tls",
  proxy: "egress.outcome.proxy",
  proxy_auth: "egress.outcome.proxyAuth",
  http_error: "egress.outcome.httpError",
  slow: "egress.outcome.slow",
  network: "egress.outcome.network",
};

export function egressOutcomeMessage(result) {
  const key = OUTCOME_KEYS[result?.outcome];
  if (key) return i18n.t(key, { status: result.status || "", kbps: result.throughput_kbps || 0 });
  return result?.error || i18n.t("egress.outcome.unknown");
}

export function egressThroughputLabel(result) {
  if (!result?.throughput_kbps) return "";
  const kbps = result.throughput_kbps;
  const rate = kbps >= 1000 ? `${(kbps / 1000).toFixed(1)} Mbps` : `${kbps} kbps`;
  return i18n.t("egress.result.throughput", { rate, ttfb: result.ttfb_ms ?? 0 });
}
