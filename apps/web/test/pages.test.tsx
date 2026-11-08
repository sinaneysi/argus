// Server-render smoke tests: each page renders from the API's real shapes (fixtures captured from the demo data).
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import authOptions from "./fixtures/auth-options.json";
import me from "./fixtures/me.json";
import nav from "./fixtures/nav.json";
import review from "./fixtures/review.json";
import reviewPlan from "./fixtures/review-plan.json";
import status from "./fixtures/status.json";
import today from "./fixtures/today.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => {
    const key = Object.keys(answers).find((k) => path.startsWith(k));
    return { ...(key ? answers[key]! : { status: 404, data: { error: "no fixture" } }), setCookies: [] };
  }),
  apiPost: vi.fn(),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  currentUrl: vi.fn(async () => "/today"),
  requireMe: vi.fn(async () => me),
  shellMe: vi.fn(async () => me),
  shellNav: vi.fn(async () => ({ ...answers["/api/v1/nav"]!, setCookies: [] })),
  shellStatus: vi.fn(async () => ({ ...(answers["/api/v1/status"] ?? { status: 404, data: null }), setCookies: [] })),
  field: vi.fn(),
  formCsrf: vi.fn(),
  setFlash: vi.fn(),
}));
const railCookie = { value: undefined as string | undefined };
vi.mock("next/headers", () => ({
  cookies: async () => ({ get: () => (railCookie.value ? { value: railCookie.value } : undefined) }),
}));
vi.mock("next/navigation", () => ({ usePathname: () => null, useRouter: () => ({ push: vi.fn(), refresh() {}, replace() {} }),
  redirect: (to: string) => {
    throw new Error("REDIRECT " + to);
  },
}));
vi.mock("@/components/ThemeSwitch", () => ({ ThemeSwitch: () => <form className="theme" /> }));

/** The app layout with its two slots, as Next renders it on a document load. */
async function shell(): Promise<string> {
  const { default: Layout } = await import("@/app/(app)/layout");
  const { ShellSide, ShellTop } = await import("@/components/Shell");
  return html(Layout({ children: <p>body</p>, side: <ShellSide />, top: <ShellTop /> }));
}

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
});

// prerender waits for async server components (the sidebar, the theme switch)
async function html(el: Promise<React.ReactElement> | React.ReactElement) {
  const { prelude } = await prerenderToNodeStream(await el);
  let out = "";
  for await (const chunk of prelude) out += chunk.toString();
  return noInlineStyle(out.replace(/<!-- -->/g, ""));
}

describe("pages", () => {
  it("login offers the demo accounts and posts the CSRF token", async () => {
    answers["/api/v1/me"] = { status: 401, data: { error: "login required" } };
    answers["/api/v1/auth/options"] = { status: 200, data: authOptions };
    const { default: Login } = await import("@/app/login/page");
    const out = await html(Login({ searchParams: Promise.resolve({ next: "/review" }) }));
    expect(out).toContain("Sign in as admin");
    expect(out).toContain('name="csrf" value="csrf-token-0123456789"');
    expect(out).toContain('name="next" value="/review"');
    expect(out).not.toMatch(/\sstyle="/);
  });

  it("login says why single sign-on was refused, from a code only", async () => {
    answers["/api/v1/me"] = { status: 401, data: { error: "login required" } };
    answers["/api/v1/auth/options"] = { status: 200, data: { ...authOptions, sso: true } };
    const { default: Login } = await import("@/app/login/page");
    const out = await html(Login({ searchParams: Promise.resolve({ next: "/review", sso: "norole" }) }));
    expect(out).toContain("has no role here");
    expect(out).toContain('href="/auth/login?next=%2Freview"');
    const free = await html(Login({ searchParams: Promise.resolve({ sso: "<b>free text</b>" }) }));
    expect(free).not.toContain("free text");
    const proto = await html(Login({ searchParams: Promise.resolve({ sso: "constructor" }) }));
    expect(proto).not.toContain('role="alert"');
  });

  it("old URLs of pages merged into Review redirect", async () => {
    const { default: config } = await import("../next.config");
    const r = await config.redirects!();
    expect(r.map((x) => x.source).sort()).toEqual(["/admin/lessons", "/ideas", "/problems", "/proposals"]);
    expect(r.every((x) => x.destination.startsWith("/review?kind=") && "statusCode" in x && x.statusCode === 301)).toBe(true);
  });

  it("only the API's own paths go to the API: no fallback to Go pages", async () => {
    const { default: config } = await import("../next.config");
    const r = await config.rewrites!();
    const list = (Array.isArray(r) ? r : r.afterFiles) ?? [];
    expect(list.map((x) => x.source).sort()).toEqual(["/api/:path*", "/apple-touch-icon.png", "/auth/:path*", "/cicd/summary.json", "/favicon.ico",
      "/ideas/prompt.md", "/inventory.csv", "/rca/prompt.md", "/static/:path*"]);
  });

  it("login sends a signed-in visitor on", async () => {
    answers["/api/v1/me"] = { status: 200, data: me };
    answers["/api/v1/auth/options"] = { status: 200, data: authOptions };
    const { default: Login } = await import("@/app/login/page");
    await expect(Login({ searchParams: Promise.resolve({ next: "https://evil.example" }) })).rejects.toThrow("REDIRECT /today");
  });

  it("today renders the day bar, Needs you, the plan and the Review card", async () => {
    answers["/api/v1/today"] = { status: 200, data: today };
    const { default: Today } = await import("@/app/(app)/today/page");
    const out = await html(Today({ searchParams: Promise.resolve({}) }));
    expect(out).toContain("<h1>Today</h1>");
    expect(out).toContain("Needs you");
    expect(out).toContain("Plan for today");
    expect(out).toContain("Review inbox");
    expect(out).toContain(today.open[0]!.title.replace(/&/g, "&amp;"));
    expect(out).toContain('aria-label="Mark done:');
    expect(out).toMatch(/<button class="check-btn" aria-label="Mark done:/); // the empty box, not a filled Button
    expect(out).not.toMatch(/\sstyle="/);
    expect(out).toMatch(/<section data-slot="card"[^>]*aria-labelledby="needs-h"/);
    expect(out).toContain('class="finding '); // lib/shortcuts.ts ROWS: j and k move through these
    expect(out).toContain('data-cmd="replan"');
  });

  it("today tells a viewer the truth: no 'Needs you', links name where they go", async () => {
    answers["/api/v1/today"] = { status: 200, data: { ...today, admin: false, canAct: false } };
    const { default: Today } = await import("@/app/(app)/today/page");
    const out = await html(Today({ searchParams: Promise.resolve({}) }));
    expect(out).toContain("Top of today&#x27;s plan");
    expect(out).not.toContain("An admin&#x27;s next step →");
    expect(out).toContain(today.open[0]!.linkLabel);
  });

  it("the status pill names what failed and links to it", async () => {
    const { Freshness } = await import("@/components/TopBar");
    const one = { ...status, state: "failed" as const, failed: ["cert-check"], failedHref: "/runs/view?id=9" };
    const a = await html(Promise.resolve(<Freshness s={one} />));
    expect(a).toContain("cert-check not OK");
    expect(a).toContain('href="/runs/view?id=9"');
    expect(a).not.toContain("refreshed");
    const b = await html(Promise.resolve(<Freshness s={{ ...one, failed: ["a", "b"], failedHref: "/runs?status=notok" }} />));
    expect(b).toContain("2 tasks not OK");
    expect(b).toContain('href="/runs?status=notok"');
  });

  it("sidebar: group labels are not small caps, and every page has its own icon", async () => {
    const src = readFileSync(join(__dirname, "..", "components", "Sidebar.tsx"), "utf8");
    expect(src).not.toContain("small-caps");
    const { default: ia } = await import("./fixtures/nav.json");
    const links = ia.groups.flatMap((g: { links: { href: string }[] }) => g.links.map((l) => l.href));
    for (const h of links) expect(src, h).toContain(`"${h}":`);
  });

  it("API paths reach the API before the catch-all page", async () => {
    const { default: config } = await import("../next.config");
    const r = (await config.rewrites!()) as { afterFiles: { source: string }[]; fallback: unknown[] };
    expect(r.afterFiles.map((x) => x.source)).toContain("/api/:path*");
    expect(r.fallback).toEqual([]);
  });

  it("a dead link keeps a way back to its list", async () => {
    answers["/api/v1/nav"] = { status: 200, data: nav };
    const { default: NotFound } = await import("@/app/(app)/not-found");
    const out = await html(NotFound());
    expect(out).toContain("<h1>Not found</h1>");
    expect(out).toContain("data-cmdk-open");
    expect(out).toContain('href="/today"');
  });

  it("review renders the queue, filters and the selected panel with its actions", async () => {
    answers["/api/v1/review"] = { status: 200, data: review };
    const { default: Review } = await import("@/app/(app)/review/page");
    const out = await html(Review({ searchParams: Promise.resolve({ sel: "proposals:16" }) }));
    expect(out).toContain("Review inbox");
    expect(out).toContain('class="q-row on"');
    expect(out).toContain('class="rv-link"');
    expect(out).toMatch(/<section data-slot="card"[^>]*id="rv-panel"/);
    expect(out).toContain('id="rv-panel"');
    expect(out).toContain('name="sel" value="proposals:16"');
    expect(out).toMatch(/<form class="filters[^"]*"[^>]*action="\/review" method="get"/);
    expect(out).not.toMatch(/\sstyle="/);
  });

  it("review shows the plan step of an action approval", async () => {
    answers["/api/v1/review"] = { status: 200, data: reviewPlan };
    const { default: Review } = await import("@/app/(app)/review/page");
    const out = await html(Review({ searchParams: Promise.resolve({ kind: "approvals", sel: "approvals:OPS-238", step: "plan" }) }));
    expect(out).toContain("Review plan v2 before approving");
    expect(out).toContain('name="plan_version" value="2"');
    expect(out).toContain('aria-current="step"');
  });

  it("the shell lists the six groups and the freshness pill", async () => {
    answers["/api/v1/nav"] = { status: 200, data: nav };
    answers["/api/v1/status"] = { status: 200, data: status };
    const out = await shell();
    for (const g of ["Home", "Incidents", "Infrastructure", "Delivery", "Work", "Agent"]) expect(out).toContain(`>${g}<`);
    expect(out).toContain("Data as of");
    expect(out).toContain('aria-current="page"');
    expect(out).toContain('href="#nav"'); // the phone sheet opens with no script
    expect(out).toContain('id="nav"');
    expect((out.match(/<details[^>]*open/g) ?? []).length).toBe(1);
    expect(out).toMatch(/<button[^>]*name="rail"[^>]*value="on"|<button[^>]*value="on"[^>]*name="rail"/);
    expect(out).not.toContain("data-rail");
  });

  it("the rail cookie opens every group, keeps every label for a screen reader, and offers the way back", async () => {
    answers["/api/v1/nav"] = { status: 200, data: nav };
    answers["/api/v1/status"] = { status: 200, data: status };
    railCookie.value = "on";
    const out = await shell();
    railCookie.value = undefined;
    expect(out).toContain('data-rail="on"');
    expect((out.match(/<details[^>]*open/g) ?? []).length).toBe(nav.groups.length);
    expect(out).toMatch(/<button[^>]*name="rail"[^>]*value="off"|<button[^>]*value="off"[^>]*name="rail"/);
    expect(out).toContain("rail:sr-only");
  });
});
