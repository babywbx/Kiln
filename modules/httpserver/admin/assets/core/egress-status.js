import { i18n } from "/admin/assets/core/i18n.js";

const OUTCOME_KEYS = {
  blocked: "egress.outcome.blocked",
  dns: "egress.outcome.dns",
  timeout: "egress.outcome.timeout",
  tls: "egress.outcome.tls",
  proxy: "egress.outcome.proxy",
  proxy_auth: "egress.outcome.proxyAuth",
  http_error: "egress.outcome.httpError",
  network: "egress.outcome.network",
};

export function egressOutcomeMessage(result) {
  const key = OUTCOME_KEYS[result?.outcome];
  if (key) return i18n.t(key, { status: result.status || "" });
  return result?.error || i18n.t("egress.outcome.unknown");
}
