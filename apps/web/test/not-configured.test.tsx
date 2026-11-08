// The shared "Connect X to enable this page" state (ADR-0021 D5): the page's header and one empty-state card with the
// marker the clean-install test reads; the Settings link only for admins.
import { prerenderToNodeStream } from "react-dom/static";
import { describe, expect, it, vi } from "vitest";
import { noInlineStyle } from "./no-inline-style";

let needs: { status: number; data: unknown } = { status: 200, data: { kinds: [] } };
let role = "admin";
const asked: string[] = [];
vi.mock("@/lib/server", () => ({
  apiGet: vi.fn(async (path: string) => { asked.push(path); return { ...needs, setCookies: [] }; }),
  shellMe: vi.fn(async () => ({ role })),
}));

const { notConfigured } = await import("@/components/NotConfigured");
const CicdPage = (await import("@/app/(app)/cicd/page")).default;

async function html(el: React.ReactElement) {
  const { prelude } = await prerenderToNodeStream(el);
  let out = "";
  for await (const chunk of prelude) out += chunk.toString();
  return noInlineStyle(out.replace(/<!-- -->/g, ""));
}

const gitlab = { key: "gitlab", label: "GitLab", settings: "/admin/sources", hint: "or set GITLAB_URL" };

describe("not configured", () => {
  it("is null when the page's sources are connected", async () => {
    needs = { status: 200, data: { kinds: [] } };
    expect(await notConfigured("/cicd", "CI/CD")).toBeNull();
    expect(asked.at(-1)).toBe("/api/v1/needs?page=%2Fcicd");
  });
  it("shows the header and the connect card, with the link for an admin", async () => {
    needs = { status: 200, data: { kinds: [], notConfigured: { sources: [gitlab] } } };
    const out = await html(await CicdPage({ searchParams: Promise.resolve({}) }) as React.ReactElement);
    expect(out).toContain("<h1>CI/CD</h1>");
    expect(out).toContain('data-state="not-configured"');
    expect(out).toContain("Connect GitLab to enable this page.");
    expect(out).toContain('href="/admin/sources"');
  });
  it("tells a viewer who connects it, without a link", async () => {
    role = "viewer";
    needs = { status: 200, data: { kinds: [], notConfigured: { sources: [{ ...gitlab, key: "claude", label: "Claude", settings: "/admin/claude" }] } } };
    const out = await html((await notConfigured("/rca", "RCA library"))!);
    expect(out).toContain("An admin connects it in Settings → Claude.");
    expect(out).not.toContain("href=");
  });
});
