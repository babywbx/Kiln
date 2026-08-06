import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import type { CanvasKit, FontMgr, TextStyle } from "canvaskit-wasm/full";
import charsetText from "../../fonts/og-charset.txt?raw";

const nodeRequire = createRequire(import.meta.url);

const FONT_FILES = ["./public/fonts/Inter-Bold.ttf", "./src/fonts/NotoSansSC-Bold-subset.otf"];
const FAMILIES = ["Inter", "Noto Sans CJK SC"];

const WIDTH = 1200;
const HEIGHT = 630;
const PADDING = 88;
const MEASURE = 900;

const SECTIONS: Record<string, { zh: string; en: string }> = {
  start: { zh: "入门", en: "Getting started" },
  guide: { zh: "指南", en: "Guides" },
  reference: { zh: "参考", en: "Reference" },
};
const SITE_LABEL = { zh: "Kiln 文档", en: "Kiln Docs" };

export function eyebrowFor(id: string): string {
  const isEnglish = id === "en" || id.startsWith("en/");
  const lang = isEnglish ? "en" : "zh";
  const parts = (isEnglish ? id.slice(3) : id).split("/");
  const labels = parts.length > 1 ? SECTIONS[parts[0] ?? ""] : undefined;
  return labels ? labels[lang] : SITE_LABEL[lang];
}

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

interface Renderer {
  ck: CanvasKit;
  fonts: FontMgr;
}

let renderer: Promise<Renderer> | undefined;

function load(): Promise<Renderer> {
  renderer ??= (async (): Promise<Renderer> => {
    const { default: init } = await import("canvaskit-wasm/full");
    const ck = await init({
      locateFile: (file: string) => nodeRequire.resolve(`canvaskit-wasm/bin/full/${file}`),
    });
    const files = await Promise.all(
      FONT_FILES.map(async (file): Promise<ArrayBuffer> => {
        const data = await readFile(file);
        return data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength);
      }),
    );
    const fonts = ck.FontMgr.FromData(...files);
    if (!fonts) throw new Error("OG card fonts failed to load");
    return { ck, fonts };
  })();
  return renderer;
}

export interface OgCard {
  eyebrow?: string;
  title: string;
  description?: string;
}

export async function renderOgCard({
  eyebrow,
  title,
  description = "",
}: OgCard): Promise<Uint8Array<ArrayBuffer>> {
  const { ck, fonts } = await load();

  const textStyle = (
    color: [number, number, number],
    size: number,
    lineHeight: number,
  ): TextStyle => ({
    color: ck.Color(...color),
    fontFamilies: FAMILIES,
    fontSize: size,
    fontStyle: { weight: ck.FontWeight.Bold },
    heightMultiplier: lineHeight,
  });

  const surface = ck.MakeSurface(WIDTH, HEIGHT);
  if (!surface) throw new Error("OG card surface unavailable");
  const canvas = surface.getCanvas();

  const background = new ck.Paint();
  background.setShader(
    ck.Shader.MakeLinearGradient(
      [0, 0],
      [WIDTH, HEIGHT],
      [ck.Color(11, 11, 12), ck.Color(26, 26, 28)],
      null,
      ck.TileMode.Clamp,
    ),
  );
  canvas.drawRect(ck.XYWHRect(0, 0, WIDTH, HEIGHT), background);

  const edge = new ck.Paint();
  edge.setStyle(ck.PaintStyle.Stroke);
  edge.setColor(ck.Color(39, 39, 42));
  edge.setStrokeWidth(4);
  canvas.drawLine(0, 0, 0, HEIGHT, edge);

  const builder = ck.ParagraphBuilder.Make(
    new ck.ParagraphStyle({
      textAlign: ck.TextAlign.Left,
      textStyle: textStyle([250, 250, 250], 68, 1.2),
    }),
    fonts,
  );
  const spacer = (size: number): void => {
    builder.pushStyle(new ck.TextStyle({ fontSize: size, heightMultiplier: 1 }));
    builder.addText("\n\n");
  };

  if (eyebrow) {
    builder.pushStyle(new ck.TextStyle(textStyle([132, 132, 141], 28, 1.2)));
    builder.addText(eyebrow);
    spacer(20);
  }
  builder.pushStyle(new ck.TextStyle(textStyle([250, 250, 250], 68, 1.2)));
  builder.addText(title);
  if (description) {
    spacer(13);
    builder.pushStyle(new ck.TextStyle(textStyle([161, 161, 170], 31, 1.55)));
    builder.addText(description);
  }

  const paragraph = builder.build();
  paragraph.layout(MEASURE);
  const top = Math.max(PADDING, HEIGHT - PADDING - paragraph.getHeight());
  canvas.drawParagraph(paragraph, PADDING, top);

  const bytes = surface.makeImageSnapshot().encodeToBytes(ck.ImageFormat.PNG, 100);
  surface.dispose();
  if (!bytes) throw new Error("OG card encoding failed");
  return new Uint8Array(bytes);
}
