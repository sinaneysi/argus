#!/usr/bin/env node
// Contrast check of the Argus design tokens (WCAG 2.2 AA). Reads packages/design/tokens.css, resolves every
// light-dark() pair and checks each documented foreground/background pair in both themes. No dependencies.
//   node packages/design/scripts/contrast.mjs          # prints every pair, exits 1 when one fails
// Also fails when a colour token is not a light-dark() pair (the one allowed exception is --accent-contrast).
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "..", "tokens.css"), "utf8");
const root = css.slice(css.indexOf(":root {"), css.indexOf("\n}\n", css.indexOf(":root {")));
const light = {}, dark = {}, problems = [];
for (const m of root.matchAll(/--([a-z0-9-]+):\s*([^;]+);/g)) {
  const [, name, value] = m;
  const pair = value.match(/^light-dark\((#[0-9a-f]{6}),\s*(#[0-9a-f]{6})\)$/i);
  if (pair) { light[name] = pair[1]; dark[name] = pair[2]; continue; }
  if (/^#[0-9a-f]{3,8}$/i.test(value.trim())) {
    if (name === "accent-contrast") { light[name] = dark[name] = value.trim(); continue; }
    problems.push(`--${name} is a colour but not a light-dark() pair`);
  }
}
if (!/:root\[data-theme="light"\]/.test(css) || !/:root\[data-theme="dark"\]/.test(css)) problems.push("[data-theme] overrides missing");

const lum = (hex) => {
  const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map((x) => (x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
};
const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };

// [foreground, backgrounds, minimum]: 4.5 for text, 3 for focus, control boundaries and chart marks (WCAG 1.4.11)
const surfaces = ["bg", "surface", "surface-2", "hover"];
const PAIRS = [
  ...["text", "text-2", "text-muted"].map((f) => [f, surfaces, 4.5]),
  ["accent", ["bg", "surface", "surface-2"], 4.5],
  ["accent-contrast", ["accent-strong", "accent-hover"], 4.5],
  ["accent-soft-fg", ["accent-soft"], 4.5],
  ["mark-fg", ["mark-bg"], 4.5],
  ...["ok", "warn", "fail", "info"].map((k) => [`${k}-fg`, [`${k}-bg`, "surface"], 4.5]),
  ["focus", ["bg", "surface"], 3],
  ["control-border", ["bg", "surface"], 3],
  ...["data-1", "data-teal", "data-sand", "data-slate", "data-red", "data-target"].map((d) => [d, ["surface"], 3]),
];
for (const [fg, bgs, min] of PAIRS) {
  for (const bg of bgs) {
    for (const [theme, t] of [["light", light], ["dark", dark]]) {
      if (!t[fg] || !t[bg]) { problems.push(`missing token --${fg} or --${bg}`); continue; }
      const r = ratio(t[fg], t[bg]);
      const line = `${theme.padEnd(5)} --${fg} on --${bg}: ${r.toFixed(2)}:1 (min ${min})`;
      if (r < min) problems.push(line); else console.log(`ok    ${line}`);
    }
  }
}
for (const p of problems) console.log(`FAIL  ${p}`);
console.log(problems.length ? `${problems.length} problem(s)` : "all token pairs pass");
process.exit(problems.length ? 1 : 0);
