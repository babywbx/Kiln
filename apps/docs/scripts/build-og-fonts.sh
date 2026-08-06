#!/bin/sh
# Regenerate the subset CJK font used by the Open Graph card renderer.
# Run from apps/docs: `pnpm og:fonts`.
#
# OG cards only draw section labels, page titles and descriptions, so the subset
# is collected exactly from every mdx frontmatter block plus the strings in
# astro.config.ts and the card renderer. The Noto Sans CJK SC Bold source is
# fetched once from the official notofonts release into src/fonts-src/
# (gitignored) and reused on later runs. Only the subset in src/fonts/ is
# committed.
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FONTS="$ROOT/src/fonts"
SRC="$ROOT/src/fonts-src"
CHARSET="$FONTS/og-charset.txt"
SOURCE_OTF="$SRC/NotoSansCJKsc-Bold.otf"
SOURCE_URL="https://github.com/notofonts/noto-cjk/raw/main/Sans/OTF/SimplifiedChinese/NotoSansCJKsc-Bold.otf"

mkdir -p "$SRC" "$FONTS"
if [ ! -f "$SOURCE_OTF" ]; then
  echo "Fetching Noto Sans CJK SC Bold (SIL OFL) from notofonts..."
  curl -fsSL -o "$SOURCE_OTF" "$SOURCE_URL"
fi

echo "Collecting CJK glyphs from OG copy..."
node --input-type=module - "$ROOT" "$CHARSET" <<'NODE'
import { readFileSync, writeFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
const [root, out] = process.argv.slice(2);
const texts = [
  readFileSync(join(root, "astro.config.ts"), "utf8"),
  readFileSync(join(root, "src/pages/og/_og-card.ts"), "utf8"),
];
const walk = (dir) => {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) walk(path);
    else if (name.endsWith(".mdx")) {
      const match = /^---\n([\s\S]*?)\n---/.exec(readFileSync(path, "utf8"));
      if (match) texts.push(match[1]);
    }
  }
};
walk(join(root, "src/content/docs"));
const keep = (c) =>
  (c >= 0x3000 && c <= 0x30ff) ||
  (c >= 0x3400 && c <= 0x4dbf) ||
  (c >= 0x4e00 && c <= 0x9fff) ||
  (c >= 0xf900 && c <= 0xfaff) ||
  (c >= 0xff00 && c <= 0xffef);
const set = new Set();
for (const ch of texts.join("")) if (keep(ch.codePointAt(0))) set.add(ch);
writeFileSync(out, [...set].sort().join(""));
console.log(`  ${set.size} glyphs`);
NODE

echo "Subsetting..."
uv run --with fonttools pyftsubset "$SOURCE_OTF" \
  --text-file="$CHARSET" \
  --output-file="$FONTS/NotoSansSC-Bold-subset.otf" \
  --no-hinting --notdef-outline \
  --layout-features='kern,liga,calt' \
  --name-IDs='*' >/dev/null

echo "Done. Subset written to src/fonts/NotoSansSC-Bold-subset.otf."
