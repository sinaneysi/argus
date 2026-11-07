import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// The link checker of the help pages: every internal link in the docs is a page of this app or a help page.
const DOCS = join(__dirname, "../../api/internal/app/helpdocs");
const APP = join(__dirname, "../app");

function pages(dir: string, base = ""): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n);
    if (!statSync(p).isDirectory()) return n === "page.tsx" ? [base || "/"] : [];
    return pages(p, /^\(.*\)$/.test(n) ? base : `${base}/${n}`);
  });
}

describe("help links", () => {
  it("every internal link resolves", () => {
    const slugs = readdirSync(DOCS).map((f) => f.replace(/\.md$/, ""));
    const known = new Set<string | undefined>([...pages(APP), ...slugs.map((s) => `/help/${s}`), "/inventory.csv", "/cicd/summary.json"]);
    const links = readdirSync(DOCS).flatMap((f) =>
      [...readFileSync(join(DOCS, f), "utf8").matchAll(/\]\((\/[^)#?\s]*)/g)].map((m) => [f, m[1]] as const),
    );
    expect(links.length).toBeGreaterThan(50);
    expect(links.filter(([, p]) => !known.has(p))).toEqual([]);
  });
});
