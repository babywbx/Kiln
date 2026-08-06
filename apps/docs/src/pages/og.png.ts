import type { APIRoute } from "astro";
import { config } from "virtual:nimbus/config";
import { assertOgCovered, renderOgCard } from "./og/_og-card";

export const prerender = true;

export const GET: APIRoute = async () => {
  const card = { title: config.title, description: config.description ?? "" };
  assertOgCovered(`${card.title}${card.description}`, "og.png");
  return new Response(await renderOgCard(card), {
    headers: { "Content-Type": "image/png" },
  });
};
