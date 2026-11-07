// Server-render smoke tests of the Incidents section, from the API's real shapes (fixtures captured from the demo data).
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Inline, Markdown } from "@/components/Markdown";
import alerts from "./fixtures/alerts.json";
import finding from "./fixtures/finding.json";
import findings from "./fixtures/findings.json";
import incident from "./fixtures/incident.json";
import incidents from "./fixtures/incidents.json";
import problem from "./fixtures/problem.json";
import problems from "./fixtures/problems.json";
import rca from "./fixtures/rca.json";
import rcaItem from "./fixtures/rca-item.json";
import run from "./fixtures/run.json";
import today from "./fixtures/today.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => {
    const key = Object.keys(answers).sort((a, b) => b.length - a.length).find((k) => path.split("?")[0] === k);
    return { ...(key ? answers[key]! : { status: 404, data: { error: "no fixture" } }), setCookies: [] };
  }),
  apiPost: vi.fn(),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: vi.fn(),
  formCsrf: vi.fn(),
  setFlash: vi.fn(),
}));
vi.mock("next/navigation", () => ({ usePathname: () => null,
  redirect: (to: string) => { throw new Error("REDIRECT " + to); },
  notFound: () => { throw new Error("NOT FOUND"); },
}));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
});

async function html(el: Promise<React.ReactElement> | React.ReactElement) {
  const { prelude } = await prerenderToNodeStream(await el);
  let out = "";
  for await (const chunk of prelude) out += chunk.toString();
  return noInlineStyle(out.replace(/<!-- -->/g, ""));
}

const sp = (v: Record<string, string> = {}) => ({ searchParams: Promise.resolve(v) });

describe("incidents section", () => {
  it("lists incidents with filters and the admin's bulk bar", async () => {
    answers["/api/v1/incidents"] = { status: 200, data: { ...incidents, bulk: { problems: [{ value: "7", label: "P-7 · Northwind" }], back: "/incidents" } } };
    const { default: Page } = await import("@/app/(app)/incidents/page");
    const out = await html(Page(sp()));
    expect(out).toContain("<h1>Incidents</h1>");
    expect(out).toMatch(/<form class="filters"[^>]*action="\/incidents" method="get"/);
    expect(out).toContain('href="/incidents/view?id=');
    expect(out).toContain('form="pblink"');
    expect(out).not.toMatch(/\sstyle="/);
  });

  it("shows one incident with actions, fix cards, timeline and record", async () => {
    answers["/api/v1/incidents/item"] = { status: 200, data: incident };
    const { default: Page } = await import("@/app/(app)/incidents/view/page");
    const out = await html(Page(sp({ id: String(incident.id) })));
    expect(out).toContain('id="timeline"');
    expect(out).toContain('id="how-to-fix"');
    expect(out).toContain('id="record"');
    expect(out).toContain('name="csrf" value="csrf-token-0123456789"');
    expect(out).toContain("Why it works");
    expect(out).not.toMatch(/\sstyle="/);
  });

  it("the Problem panel links and creates with the CSRF token", async () => {
    const panel = { banners: [], banner: null, back: "/incidents/view?id=1#problem", ref: "incident:1", suggestions: [],
      link: { summary: "Link to problem…", pq: "", picks: [{ id: 7, title: "Northwind", status: "new" }], types: [{ value: "caused_by", label: "caused by" }], button: "Link", keep: { id: "1" }, path: "/incidents/view" },
      create: { title: "Northwind 502", noun: "incident", from: "incident:1", first: false } };
    answers["/api/v1/incidents/item"] = { status: 200, data: { ...incident, problem: panel } };
    const { default: Page } = await import("@/app/(app)/incidents/view/page");
    const out = await html(Page(sp({ id: "1" })));
    expect(out).toContain('name="ref" value="incident:1"');
    expect(out).toMatch(/name="problem"[^>]*value="7"/);
    expect(out).toContain("Create problem from this incident");
    expect(out).toContain('name="pq"');
  });

  it("a viewer gets no admin actions", async () => {
    answers["/api/v1/incidents/item"] = { status: 200, data: { ...incident, admin: false, actions: ["ack"], deep: null } };
    const { default: Page } = await import("@/app/(app)/incidents/view/page");
    const out = await html(Page(sp({ id: "1" })));
    expect(out).toContain("Acknowledge");
    expect(out).not.toContain(">Resolve<");
    expect(out).not.toContain("Investigate deeply");
  });

  it("an unknown incident is a 404", async () => {
    const { default: Page } = await import("@/app/(app)/incidents/view/page");
    await expect(Page(sp({ id: "99999" }))).rejects.toThrow("NOT FOUND");
  });

  it("renders the findings, run and finding pages", async () => {
    answers["/api/v1/incidents/findings"] = { status: 200, data: findings };
    answers["/api/v1/incidents/run"] = { status: 200, data: run };
    answers["/api/v1/incidents/finding"] = { status: 200, data: finding };
    const a = await import("@/app/(app)/incidents/problems/page");
    const b = await import("@/app/(app)/incidents/run/page");
    const c = await import("@/app/(app)/incidents/problem/page");
    expect(await html(a.default())).toContain("log-rca runs");
    expect(await html(b.default(sp({ id: "1" })))).toContain("Alerts firing at that time");
    expect(await html(c.default(sp({ run: "1", idx: "0" })))).toContain("Root cause analysis");
  });

  it("renders alerts, problems and the RCA library", async () => {
    answers["/api/v1/alerts"] = { status: 200, data: alerts };
    answers["/api/v1/problems"] = { status: 200, data: problems };
    answers["/api/v1/problems/item"] = { status: 200, data: problem };
    answers["/api/v1/rca"] = { status: 200, data: rca };
    answers["/api/v1/rca/item"] = { status: 200, data: rcaItem };
    const al = await import("@/app/(app)/alerts/page");
    const pl = await import("@/app/(app)/problems/page");
    const pv = await import("@/app/(app)/problems/view/page");
    const rl = await import("@/app/(app)/rca/page");
    const rv = await import("@/app/(app)/rca/view/page");
    expect(await html(al.default(sp()))).toContain("<h1>Alerts</h1>");
    expect(await html(pl.default(sp()))).toContain('aria-label="Problem views"');
    expect(await html(pv.default(sp({ id: "1" })))).toContain('id="linked"');
    expect(await html(rl.default(sp()))).toContain("RCA library");
    expect(await html(rv.default(sp({ id: "1" })))).toContain('id="fix-prompt"');
  });

  it("the old suggestions URL goes to Review", async () => {
    const { default: Page } = await import("@/app/(app)/problems/page");
    await expect(Page(sp({ view: "suggestions" }))).rejects.toThrow("REDIRECT /review?kind=links");
  });

  it("Today shows the checks and open incidents cards", async () => {
    answers["/api/v1/today"] = { status: 200, data: today };
    const { default: Page } = await import("@/app/(app)/today/page");
    const out = await html(Page(sp()));
    expect(out).toContain("Checks, last 48 h");
    expect(out).toContain("Active incidents");
    expect(out).not.toMatch(/\sstyle="/);
  });
});

describe("markdown", () => {
  it("keeps the report's text as text", async () => {
    const out = await html(<Markdown text={'## Cause\n<script>alert(1)</script> and <img src=x onerror=alert(1)>\n[x](javascript:alert(1))'} ids />);
    expect(out).not.toContain("<script");
    expect(out).not.toContain("<img");
    expect(out).toContain("&lt;script&gt;");
    expect(out).not.toContain('href="javascript');
    expect(out).toContain('<h3 id="s1"');
  });

  it("renders lists, fences, tables and inline marks", async () => {
    const out = await html(<Markdown text={"- one `a`\n- **two**\n\n```\n<raw>\n```\n\n| A | B |\n|---|---:|\n| proven x | 2 |\n\nsee https://example.test/a"} />);
    expect(out).toContain("<ul>");
    expect(out).toContain("<code>a</code>");
    expect(out).toContain("<strong>two</strong>");
    expect(out).toContain("&lt;raw&gt;");
    expect(out).toContain('class="pill ok"');
    expect(out).toContain('href="https://example.test/a"');
  });

  it("the fix cards only mark code", async () => {
    const out = await html(<Inline text={"**no** `yes`"} codeOnly />);
    expect(out).toContain("<code>yes</code>");
    expect(out).toContain("**no**");
  });
});
