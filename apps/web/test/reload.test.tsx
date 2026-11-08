import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh() {}, replace() {} }), usePathname: () => null }));

import { Reload, reloadOf } from "@/components/Reload";
import { Refresh } from "@/components/Section";

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    return statSync(p).isDirectory() ? files(p) : /\.tsx?$/.test(f) ? [p] : [];
  });
}

describe("auto refresh", () => {
  it("reads true, seconds and a hand-off URL", () => {
    expect(reloadOf(true)).toEqual({ seconds: 5 });
    expect(reloadOf(2)).toEqual({ seconds: 2 });
    expect(reloadOf({ seconds: 2, url: "/board/task?id=3" })).toEqual({ seconds: 2, url: "/board/task?id=3" });
    for (const off of [false, 0, null, undefined, { seconds: 0 }]) expect(reloadOf(off)).toBeNull();
  });

  it("keeps the meta refresh for no-JS only, inside noscript", () => {
    expect(renderToStaticMarkup(<Reload r={true} />)).toBe('<noscript><meta http-equiv="refresh" content="5"/></noscript>');
    expect(renderToStaticMarkup(<Reload r={{ seconds: 2, url: "/board" }} />)).toContain('content="2;url=/board"');
    expect(renderToStaticMarkup(<Refresh on />)).toContain("<noscript>");
    expect(renderToStaticMarkup(<Reload r={false} />)).toBe("");
    expect(renderToStaticMarkup(<Refresh on={false} />)).toBe("");
  });

  it("is the only place that renders a meta refresh (a bare one survives client navigation)", () => {
    const root = join(__dirname, "..");
    const bare = [...files(join(root, "app")), ...files(join(root, "components"))]
      .filter((f) => !f.endsWith(join("components", "Reload.tsx")) && /httpEquiv|http-equiv/i.test(readFileSync(f, "utf8")));
    expect(bare).toEqual([]);
  });

  it("re-renders in place with a timer that is cleared, and has no inline script", () => {
    const src = readFileSync(join(__dirname, "..", "components", "Reload.tsx"), "utf8");
    expect(src).toContain('"use client"');
    expect(src).toContain("router.refresh()");
    expect(src).toContain("clearTimeout");
    expect(src).toContain("busy(document)");
    expect(src).not.toMatch(/dangerouslySetInnerHTML|<script/);
  });
});
