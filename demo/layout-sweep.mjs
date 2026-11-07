// Layout sweep (development tool, not shipped): signs in to a running Argus as the demo admin and checks every page in
// the sidebar plus the admin and detail pages at 1440, 1280, 1024 and 390 px, with the sidebar open and collapsed.
// It reports what DESIGN.md §10 forbids and a reviewer would otherwise only see by eye:
//   covered   visible text under a form control (labels hidden by selects)
//   squeezed  text wrapped into a column narrower than 40 px
//   overflow  a block whose text runs past its box
//   hscroll   the page scrolls sideways
//   rail      an element past the edge of the sidebar
// Text hidden on purpose (screen-reader labels, phone-hidden table headers) and sticky bars are ignored.
//
//   npm i --no-save playwright-core     (or PLAYWRIGHT_CORE=/path/to/playwright-core)
//   CHROME=/usr/bin/google-chrome node demo/layout-sweep.mjs https://argus.example   # exit 1 when anything is found
const base = (process.argv[2] || "http://127.0.0.1:3000").replace(/\/+$/, "");
const pw = await import(process.env.PLAYWRIGHT_CORE || "playwright-core");
const chromium = pw.chromium ?? pw.default.chromium;
const browser = await chromium.launch({ executablePath: process.env.CHROME || "/usr/bin/google-chrome", headless: true });
const extra = ["/account", "/account/2fa", "/admin", "/admin/claude", "/admin/sources", "/admin/kube", "/admin/users", "/admin/board",
  "/admin/fix", "/admin/backup-targets", "/admin/credentials", "/inventory/view?id=7", "/incidents/view?id=145"];
const found = [];

function check() {
  const out = [];
  document.querySelectorAll("details").forEach((d) => { d.open = true; });
  const shown = (e) => {
    const s = getComputedStyle(e), r = e.getBoundingClientRect();
    if (s.visibility === "hidden" || s.display === "none" || r.width <= 2 || r.height <= 2) return false;
    for (let a = e; a && a !== document.body; a = a.parentElement) {  // inside a screen-reader-only box (a phone-hidden thead)
      const q = getComputedStyle(a), w = a.getBoundingClientRect();
      if (q.clip !== "auto" || q.clipPath !== "none" || ((w.width <= 2 || w.height <= 2) && q.overflow !== "visible")) return false;
    }
    return true;
  };
  const sticky = (e) => { for (let a = e; a; a = a.parentElement) { const p = getComputedStyle(a).position; if (p === "sticky" || p === "fixed") return true; } return false; };
  const main = document.querySelector("main") || document.body;
  const controls = [...main.querySelectorAll("input:not([type=hidden]):not([type=checkbox]):not([type=radio]),select,textarea,button")].filter((c) => shown(c) && !sticky(c));
  const texts = [...main.querySelectorAll("span,label,p,b,a,h2,h3,td,th,dt,dd,li")]
    .filter((e) => shown(e) && !sticky(e) && [...e.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim().length > 1));
  const name = (e) => `"${e.textContent.trim().replace(/\s+/g, " ").slice(0, 32)}"`;
  for (const t of texts) {
    const a = t.getBoundingClientRect();
    for (const c of controls) {
      if (c.contains(t) || t.contains(c)) continue;
      const b = c.getBoundingClientRect();
      if (Math.min(a.right, b.right) - Math.max(a.left, b.left) > 6 && Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 6) { out.push(`covered ${name(t)} by ${c.tagName.toLowerCase()}`); break; }
    }
    if (a.width < 40 && t.textContent.trim().length > 12 && a.height > 60) out.push(`squeezed ${name(t)} ${Math.round(a.width)}px`);
    if (!["A", "B", "SPAN"].includes(t.tagName) && getComputedStyle(t).overflowX === "visible" && t.clientWidth > 0 && t.scrollWidth > t.clientWidth + 2) out.push(`overflow ${name(t)}`);
  }
  if (document.documentElement.scrollWidth > innerWidth + 1) out.push(`hscroll ${document.documentElement.scrollWidth}px`);
  const side = document.querySelector("[data-slot=sidebar]");
  if (side && getComputedStyle(side).visibility !== "hidden") {
    const r = side.getBoundingClientRect();
    for (const e of side.querySelectorAll("*")) { const q = e.getBoundingClientRect(); if (shown(e) && q.right > r.right + 1) { out.push(`rail ${e.tagName.toLowerCase()}.${String(e.className).split(" ")[0]}`); break; } }
  }
  return [...new Set(out)];
}

for (const rail of [false, true]) {
  for (const width of rail ? [1440, 1024] : [1440, 1280, 1024, 390]) {
    const ctx = await browser.newContext({ viewport: { width, height: 900 } });
    if (rail) await ctx.addCookies([{ name: "argus_sidebar", value: "on", url: base }]);
    const page = await ctx.newPage();
    await page.goto(`${base}/login?next=/today`);
    await Promise.all([page.waitForURL((u) => u.pathname === "/today", { timeout: 30000 }),
      page.locator('form:has(input[value="demo.admin"]) button').first().click({ timeout: 30000 })]);
    const nav = await page.evaluate(() => [...new Set([...document.querySelectorAll('[data-slot=sidebar] a[href^="/"]')].map((a) => a.getAttribute("href").split("#")[0]))]);
    for (const path of [...new Set([...nav, ...extra])]) {
      try { await page.goto(base + path, { waitUntil: "load", timeout: 30000 }); } catch { found.push(`${path} ${width}${rail ? " rail" : ""}: did not load`); continue; }
      for (const f of await page.evaluate(check)) found.push(`${path} ${width}${rail ? " rail" : ""}: ${f}`);
    }
    await ctx.close();
  }
}
await browser.close();
console.log(found.length ? found.join("\n") : "layout sweep: 0 findings");
process.exit(found.length ? 1 : 0);
