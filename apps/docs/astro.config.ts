import { defineConfig } from "astro/config";
import icon from "astro-icon";
import tailwindcss from "@tailwindcss/vite";
import nimbus, { defineConfig as defineNimbusConfig } from "@cloudflare/nimbus-docs";
import { tableScroll } from "@cloudflare/nimbus-docs/markdown";

const nimbusConfig = defineNimbusConfig({
  site: "https://kiln.wbxdocs.com",
  title: "Kiln",
  description: "自托管 HLS 与 DASH 频道网关：原生解密重封装、按需拉流、统一鉴权入口。",
  locale: "zh-CN",
  github: "https://github.com/babywbx/kiln",
  socialImageAlt: "Kiln 文档预览",
  sidebar: {
    scope: "section",
    items: [
      { label: "入门", autogenerate: { directory: "start" } },
      { label: "指南", autogenerate: { directory: "guide" } },
      { label: "参考", autogenerate: { directory: "reference" } },
      {
        label: "English",
        items: [
          { label: "Getting started", autogenerate: { directory: "en/start" } },
          { label: "Guides", autogenerate: { directory: "en/guide" } },
          { label: "Reference", autogenerate: { directory: "en/reference" } },
        ],
      },
    ],
  },
});

export default defineConfig({
  output: "static",
  // Tailwind v4 via its Vite plugin (the integration Astro recommends for
  // Tailwind v4 — replaces the PostCSS plugin, which doesn't build under
  // Astro 7's Vite 8 bundler).
  vite: {
    plugins: [tailwindcss()],
  },
  // Hover-prefetch link targets so full-page navigations feel instant without
  // a client-side router.
  prefetch: {
    prefetchAll: true,
    defaultStrategy: "hover",
  },
  integrations: [
    icon(),
    nimbus(nimbusConfig, {
      // Authoring rules are opt-in by design — your repo, your taste. The
      // two below are the load-bearing pair: frontmatter has to validate
      // against the content schema for the page to render properly, and
      // broken internal links are 404s for your readers. Add the others
      // (heading hierarchy, code-block language, style, etc.) when you're
      // ready to enforce them — see `nimbus-docs lint --help`.
      rules: {
        "nimbus/frontmatter-shape": "error",
        "nimbus/internal-link": "error",
      },
      // Wrap wide tables so they scroll instead of overflowing the page
      // (styled by `.nb-table-scroll` in src/styles/prose.css).
      markdown: {
        hastPlugins: [tableScroll()],
      },
    }),
  ],
});
