import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// Text from the API is rendered by React, which escapes it. This guard replaces the Go pages' "escape everything"
// tests: no component may hand raw HTML to the browser.
function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n);
    if (statSync(p).isDirectory()) return sources(p);
    return /\.tsx?$/.test(n) ? [p] : [];
  });
}

describe("no raw HTML", () => {
  it("has no dangerouslySetInnerHTML in the app", () => {
    const files = ["app", "components", "lib"].flatMap((d) => sources(join(__dirname, "..", d)));
    expect(files.length).toBeGreaterThan(50);
    expect(files.filter((f) => readFileSync(f, "utf8").includes("dangerouslySetInnerHTML"))).toEqual([]);
  });
});
