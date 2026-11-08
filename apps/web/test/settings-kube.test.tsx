// Settings → Kubernetes from the API's real shape (captured as the admin), then its writes through the Settings actions.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import kube from "./fixtures/admin-kube.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown> }[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/admin/kube?kr=1" } };

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => ({ ...(answers[path.split("?")[0]!] ?? { status: 404, data: {} }), setCookies: [], path })),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>) => { posted.push({ path, body }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? "").slice(0, 2000),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async () => undefined),
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh() {}, replace() {} }), usePathname: () => null, redirect: (to: string) => { throw new Error("REDIRECT " + to); }, notFound: () => { throw new Error("NOT FOUND"); } }));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  posted.length = 0;
  answer = { status: 200, data: { ok: true, redirect: "/admin/kube?kr=1" } };
});

async function html(el: React.ReactNode): Promise<string> {
  const { prelude } = await prerenderToNodeStream(el);
  const parts: Buffer[] = [];
  for await (const c of prelude) parts.push(Buffer.from(c as Uint8Array));
  return noInlineStyle(Buffer.concat(parts).toString("utf8").replace(/<!-- -->/g, ""));
}
const render = async (d: unknown, q: Record<string, string> = {}) => {
  answers["/api/v1/admin/kube"] = { status: 200, data: d };
  const { default: Page } = await import("@/app/(app)/admin/kube/page");
  return html(await Page({ searchParams: Promise.resolve(q) }));
};
const FAKE = "not-a-real-value"; // stands for a pasted kubeconfig

describe("Kubernetes", () => {
  it("shows the status, each cluster with its actions, the add form and the help", async () => {
    const out = await render(kube);
    const c = kube.clusters[0]!;
    for (const w of ["<h1>Kubernetes</h1>", 'id="kube-now"', `id="kube-c-${c.name}"`, "Check again", `/admin/kube?replace=${c.name}#kube-add`, `Remove ${c.name}`, "Add a cluster", 'name="kubeconfig"', 'name="kubeconfig_file"', 'type="file"', 'name="context"', 'name="override"', "Save and check", "argus-ro.yaml", "kubectl apply"])
      expect(out).toContain(w);
    expect(out).toMatch(/<textarea[^>]*name="kubeconfig"[^>]*><\/textarea>/);
  });
  it("opens Replace with the cluster's name and environment", async () => {
    const c = kube.clusters[0]!;
    const out = await render({ ...kube, add: { replace: c.name, env: "develop", open: true } }, { replace: c.name });
    expect(out).toContain(`Replace ${c.name}`);
    expect(out.match(/<input[^>]*name="name"[^>]*>/)?.[0]).toContain(`value="${c.name}"`);
    expect(out.match(/<input[^>]*value="develop"[^>]*>/)?.[0]).toContain("checked");
  });
  it("is for admins", async () => {
    answers["/api/v1/admin/kube"] = { status: 403, data: { error: "Admins only" } };
    const { default: Page } = await import("@/app/(app)/admin/kube/page");
    const out = await html(await Page({ searchParams: Promise.resolve({}) }));
    expect(out).toContain("Admins only");
    expect(out).not.toContain("<form");
  });
  it("sends a chosen file as text, and a refusal keeps neither the paste nor the file", async () => {
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    const fd = () => {
      const f = new FormData();
      f.set("_to", "kube"); f.set("action", "save"); f.set("name", "dev"); f.set("env", "develop"); f.set("kubeconfig", FAKE);
      f.set("kubeconfig_file", new File([FAKE + "-file"], "k.yaml"));
      return f;
    };
    await expect(settingsAct(null, fd())).rejects.toThrow("REDIRECT /admin/kube?kr=1");
    expect(posted[0]).toEqual({ path: "/api/v1/admin/kube", body: { action: "save", name: "dev", env: "develop", kubeconfig: FAKE, kubeconfig_file: FAKE + "-file" } });
    answer = { status: 400, data: { error: "Exec plugins are refused.", field: "kubeconfig" } };
    const st = await settingsAct(null, fd());
    expect(st).toMatchObject({ field: "kubeconfig", values: { name: "dev", env: "develop" } });
    expect(JSON.stringify(st)).not.toContain(FAKE);
  });
  it("skips an empty file field", async () => {
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    const f = new FormData();
    f.set("_to", "kube"); f.set("kubeconfig_file", new File([], ""));
    await expect(settingsAct(null, f)).rejects.toThrow("REDIRECT");
    expect(posted[0]!.body).toEqual({});
  });
});
