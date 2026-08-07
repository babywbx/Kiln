import { config } from "virtual:nimbus/config";

export const prerender = true;

const AI_AGENTS = [
  "GPTBot",
  "OAI-SearchBot",
  "ChatGPT-User",
  "ClaudeBot",
  "Claude-User",
  "Claude-SearchBot",
  "PerplexityBot",
  "Perplexity-User",
  "Google-Extended",
  "Applebot-Extended",
  "meta-externalagent",
  "Amazonbot",
  "Bytespider",
  "CCBot",
  "cohere-ai",
];

export function GET() {
  const llms = new URL("/llms.txt", config.site).href;
  const llmsFull = new URL("/llms-full.txt", config.site).href;
  const body = [
    `# Plain-text corpus for language models: ${llms} (index)`,
    `# and ${llmsFull} (every page in one document).`,
    "",
    "User-agent: *",
    "Allow: /",
    "",
    "# Reading and training on these docs is welcome.",
    ...AI_AGENTS.map((agent) => `User-agent: ${agent}`),
    "Allow: /",
    "",
    `Sitemap: ${new URL("/sitemap-index.xml", config.site).href}`,
    "",
  ].join("\n");

  return new Response(body, {
    headers: { "Content-Type": "text/plain; charset=utf-8" },
  });
}
