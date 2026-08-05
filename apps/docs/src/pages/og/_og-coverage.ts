import charsetText from "../../fonts/og-charset.txt?raw";

const charset = new Set(charsetText);

function isCjk(codePoint: number): boolean {
  return (
    (codePoint >= 0x3000 && codePoint <= 0x30ff) ||
    (codePoint >= 0x3400 && codePoint <= 0x4dbf) ||
    (codePoint >= 0x4e00 && codePoint <= 0x9fff) ||
    (codePoint >= 0xf900 && codePoint <= 0xfaff) ||
    (codePoint >= 0xff00 && codePoint <= 0xffef)
  );
}

export function assertOgCovered(text: string, context: string): void {
  for (const char of text) {
    const codePoint = char.codePointAt(0);
    if (codePoint !== undefined && isCjk(codePoint) && !charset.has(char)) {
      throw new Error(
        `OG font subset is missing "${char}" (${context}). Run \`pnpm og:fonts\` to regenerate.`,
      );
    }
  }
}
