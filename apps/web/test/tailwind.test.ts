import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { noInlineStyle } from "./no-inline-style";

const read = (p: string) => readFileSync(join(__dirname, "..", p), "utf8");

describe("Tailwind containment", () => {
  const css = read("app/tailwind.css");
  // Comments mention these names, so only the rules count.
  const rules = css.replace(/\/\*[\s\S]*?\*\//g, "");

  it("has no Preflight, which would restyle every existing page", () => {
    expect(rules).not.toMatch(/preflight/i);
    expect(rules).not.toMatch(/@import\s+["']tailwindcss["']/);
  });

  it("scans only the listed folders, so old class names generate no CSS", () => {
    expect(rules).toContain('@import "tailwindcss/utilities.css" layer(utilities) source(none);');
  });

  it("puts the old element reset under the utilities", () => {
    expect(rules).toMatch(/^\s*@layer argus-reset, theme, base, components, utilities;/);
    const bare = read("app/base.css").replace(/@layer argus-reset \{[\s\S]*?\n\}/, "");
    expect(bare).not.toMatch(/^h1, h2, h3/m);
    expect(bare).not.toMatch(/^button, input, select, textarea/m);
  });

  it("ends on a real rule, so no unterminated @layer swallows the next file in the chunk", () => {
    expect(rules.trimEnd().endsWith("}")).toBe(true);
    expect(rules.slice(rules.lastIndexOf("@layer"))).toContain("[data-slot=");
  });

  it("keeps colour literals in tokens.css", () => {
    expect(rules).not.toMatch(/#[0-9a-f]{3,8}\b|\b(?:rgb|hsl|oklch)a?\(/i);
  });
});

describe("Base UI under the CSP", () => {
  it("turns Base UI's <style> off in the root layout and passes no nonce", () => {
    const layout = read("app/layout.tsx");
    expect(layout).toContain("<CSPProvider disableStyleElements>");
    expect(layout).not.toMatch(/<CSPProvider[^>]*nonce/);
  });
});

describe("noInlineStyle", () => {
  it("finds a style attribute and a style element", () => {
    expect(() => noInlineStyle('<main><div class="a" style="width:1px"></div></main>')).toThrow(/breaks the CSP/);
    expect(() => noInlineStyle("<style>a{}</style>")).toThrow(/breaks the CSP/);
  });

  it("passes escaped text and other attributes", () => {
    const ok = '<p data-style="x">style=&quot;a&quot; &lt;style&gt;</p>';
    expect(noInlineStyle(ok)).toBe(ok);
  });
});
