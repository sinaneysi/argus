import { describe, expect, it } from "vitest";
import { actionCommands, GO_KEYS, goCommands, keyName, searchHref } from "@/lib/shortcuts";
import type { Nav } from "@/lib/types";
import navFixture from "./fixtures/nav.json";

const nav = navFixture as unknown as Nav;

describe("shortcuts", () => {
  it("gives every jump its own key, on a page the nav has", () => {
    const keys = Object.values(GO_KEYS);
    expect(new Set(keys).size).toBe(keys.length);
    const hrefs = goCommands(nav).map((c) => c.href);
    for (const href of Object.keys(GO_KEYS)) expect(hrefs).toContain(href);
  });

  it("offers only the pages the nav gives this role", () => {
    const small = { ...nav, groups: nav.groups.filter((g) => g.key === "home") };
    expect(goCommands(small).map((c) => c.href)).not.toContain("/incidents");
    expect(actionCommands(small, false, true).map((a) => a.id)).not.toContain("new-task");
  });

  it("keeps Re-plan for admins and names the toggle by its state", () => {
    expect(actionCommands(nav, false, true).map((a) => a.id)).not.toContain("replan");
    expect(actionCommands(nav, true, true).map((a) => a.id)).toContain("replan");
    expect(actionCommands(nav, true, false).find((a) => a.id === "toggle")?.label).toContain("on");
  });

  it("names keys as the tables do, and builds the search address", () => {
    expect(keyName("R", true)).toBe("Shift R");
    expect(keyName("?", true)).toBe("?");
    expect(searchHref(nav, "pay 01")).toBe("/inventory?go=1&q=pay+01");
  });
});
