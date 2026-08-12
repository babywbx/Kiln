export function expectedPublicBaseScheme(tlsEnabled, splitListener) {
  if (!tlsEnabled) return "http:";
  return splitListener ? null : "https:";
}
