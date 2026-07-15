import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const css = await readFile("modules/httpserver/admin/assets/app.css", "utf8");

test("checkboxes keep fixed geometry and do not inherit text-field hover paint", () => {
  assert.match(css, /input:not\(\[type="checkbox"\]\),\s*select,\s*textarea\s*\{/);
  assert.match(css, /input:not\(\[type="checkbox"\]\):hover,\s*select:hover,\s*textarea:hover\s*\{/);

  const checkbox = css.match(/\.check-row input\[type="checkbox"\]\s*\{([^}]*)\}/);
  assert.ok(checkbox, "expected a dedicated checkbox rule");
  assert.match(checkbox[1], /appearance:\s*none/);
  assert.match(checkbox[1], /width:\s*17px/);
  assert.match(checkbox[1], /height:\s*17px/);
  assert.match(checkbox[1], /padding:\s*0/);
  assert.match(css, /\.check-row input\[type="checkbox"\]:checked\s*\{/);
});
