import { getCollection } from "astro:content";
import { OGImageRoute } from "astro-og-canvas";
import { ogCardConfig } from "./_og-card-config";
import { assertOgCovered } from "./_og-coverage";

const entries = await getCollection("docs", (entry) => !entry.data.draft);

const pages = Object.fromEntries(
  entries.map((entry) => [
    entry.id,
    {
      title: entry.data.title,
      description: entry.data.description ?? "",
    },
  ]),
);

export const { getStaticPaths, GET } = await OGImageRoute({
  pages,
  getImageOptions: (path, page) => {
    assertOgCovered(`${page.title}${page.description}`, path);
    return {
      title: page.title,
      description: page.description,
      ...ogCardConfig,
    };
  },
});
