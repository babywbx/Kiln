import type { APIRoute, GetStaticPaths } from "astro";
import { getCollection } from "astro:content";
import { assertOgCovered, eyebrowFor, renderOgCard, type OgCard } from "./_og-card";

export const getStaticPaths: GetStaticPaths = async () => {
  const entries = await getCollection("docs", (entry) => !entry.data.draft);
  return entries.map((entry) => ({
    params: { slug: `${entry.id}.png` },
    props: {
      card: {
        eyebrow: eyebrowFor(entry.id),
        title: entry.data.title,
        description: entry.data.description ?? "",
      },
      context: entry.id,
    },
  }));
};

export const GET: APIRoute<{ card: OgCard; context: string }> = async ({ props }) => {
  const { card, context } = props;
  assertOgCovered(`${card.eyebrow ?? ""}${card.title}${card.description ?? ""}`, context);
  return new Response(await renderOgCard(card), {
    headers: { "Content-Type": "image/png" },
  });
};
