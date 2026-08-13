export function errorSummary(message) {
  const limit = 20;
  const text = String(message || "").trim();
  const head = text.split(": ")[0].trim() || text;
  return head.length > limit ? `${head.slice(0, limit - 1)}…` : head;
}
