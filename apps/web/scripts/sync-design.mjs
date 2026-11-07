// Copies the self-hosted fonts and the favicon set from packages/design into public/static (gitignored), where
// tokens.css expects them (/static/fonts/...). Run before dev and build.
import { cpSync, mkdirSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const design = join(here, "../../../packages/design");
const out = join(here, "../public/static");
mkdirSync(join(out, "fonts"), { recursive: true });
for (const f of readdirSync(join(design, "static/fonts"))) {
  if (f.endsWith(".woff2") || f.endsWith(".txt")) cpSync(join(design, "static/fonts", f), join(out, "fonts", f));
}
for (const f of ["favicon.svg", "favicon-32.png", "favicon.ico", "apple-touch-icon.png"]) {
  cpSync(join(design, "static", f), join(out, f));
}
for (const f of ["mark.svg", "mark-dark.svg"]) cpSync(join(design, "brand", f), join(out, f));
console.log("design assets copied to public/static");
