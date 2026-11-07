import { describe, expect, it } from "vitest";
import { appPath } from "@/components/Soft";

const base = "https://argus.example/today";

describe("client navigation", () => {
  it("takes pages of the app, with their query and fragment", () => {
    expect(appPath("/incidents", base)?.pathname).toBe("/incidents");
    expect(appPath("/runs?page=2#runs", base)?.href).toBe("https://argus.example/runs?page=2#runs");
    expect(appPath("view?id=3", "https://argus.example/incidents/")?.pathname).toBe("/incidents/view");
  });

  it("leaves API routes, files, sign-in, Two-factor and other sites to the browser", () => {
    for (const h of ["/api/v1/nav", "/auth/oidc", "/static/app.css", "/_next/x.js", "/healthz", "/login?next=/today", "/logout",
      "/account/2fa", "/inventory.csv", "/cicd/summary.json", "/rca/prompt.md", "https://other.example/today", "mailto:a@b.c"]) {
      expect(appPath(h, base), h).toBeNull();
    }
    expect(appPath("/account", base)?.pathname).toBe("/account");
  });
});
