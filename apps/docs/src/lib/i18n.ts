/** Chinese lives at the root; English mirrors it under /en. */
export function isEnglishPath(pathname: string): boolean {
  return pathname === "/en" || pathname.startsWith("/en/");
}
