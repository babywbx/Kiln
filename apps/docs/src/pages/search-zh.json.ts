import { getCollection } from "astro:content";
import type { APIRoute } from "astro";

interface HeadingEntry {
  t: string;
  s: string;
}

interface PageEntry {
  u: string;
  t: string;
  h: HeadingEntry[];
}

function slugify(text: string): string {
  return text
    .trim()
    .toLocaleLowerCase("zh-Hans")
    .replace(/[^\p{L}\p{N}\s_-]/gu, "")
    .replace(/\s+/g, "-");
}

function cleanHeading(raw: string): string {
  return raw
    .replace(/`([^`]*)`/g, "$1")
    .replace(/\*\*([^*]*)\*\*/g, "$1")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .trim();
}

export const GET: APIRoute = async () => {
  const entries = await getCollection(
    "docs",
    (entry) => !entry.id.startsWith("en/") && entry.data.draft !== true,
  );

  const pages: PageEntry[] = entries.map((entry) => {
    const url = `/${entry.id}/`;
    const headings: HeadingEntry[] = [];
    let inFence = false;
    for (const line of (entry.body ?? "").split("\n")) {
      if (line.startsWith("```")) {
        inFence = !inFence;
        continue;
      }
      if (inFence) continue;
      const match = /^#{2,3}\s+(.+)$/.exec(line);
      if (!match) continue;
      const text = cleanHeading(match[1]);
      if (text) headings.push({ t: text, s: slugify(text) });
    }
    return { u: url, t: entry.data.title, h: headings };
  });

  return new Response(JSON.stringify(pages), {
    headers: { "Content-Type": "application/json; charset=utf-8" },
  });
};
