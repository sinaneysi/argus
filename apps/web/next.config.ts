import path from "node:path";
import type { NextConfig } from "next";

// Security headers that do not depend on the request; the CSP with its per-request nonce is set in middleware.ts.
const securityHeaders = [
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "Referrer-Policy", value: "same-origin" },
  { key: "X-Frame-Options", value: "DENY" },
  { key: "Permissions-Policy", value: "camera=(), microphone=(), geolocation=(), interest-cohort=()" },
  { key: "Cross-Origin-Opener-Policy", value: "same-origin" },
];

const apiUrl = (process.env.API_URL ?? "http://api:8080").replace(/\/+$/, "");

const config: NextConfig = {
  output: "standalone",
  outputFileTracingRoot: path.join(__dirname, "../.."),
  poweredByHeader: false,
  reactStrictMode: true,
  productionBrowserSourceMaps: false,
  images: { unoptimized: true }, // no image optimiser (and no sharp/libvips in the image)
  // Old URLs of pages that moved into Review (docs/architecture/argus-2/ia.md).
  async redirects() {
    return [
      { source: "/proposals", destination: "/review?kind=proposals", statusCode: 301 },
      { source: "/ideas", destination: "/review?kind=ideas", statusCode: 301 },
      { source: "/admin/lessons", destination: "/review?kind=lessons", statusCode: 301 },
      { source: "/problems", has: [{ type: "query", key: "view", value: "suggestions" }], destination: "/review?kind=links", statusCode: 301 },
    ];
  },
  // Next serves every page; only these paths belong to the API service, so one port serves the whole product when no
  // edge routes them (the demo's edge routes them to the API directly, docs/DEPLOY-DEMO.md).
  async rewrites() {
    const api = ["/api/:path*", "/auth/:path*", "/static/:path*", "/favicon.ico", "/apple-touch-icon.png", "/inventory.csv",
      "/cicd/summary.json", "/rca/prompt.md", "/ideas/prompt.md"];
    // afterFiles, not fallback: the catch-all page (app/(app)/[...missing]) would answer these first.
    return { beforeFiles: [], fallback: [], afterFiles: api.map((source) => ({ source, destination: `${apiUrl}${source}` })) };
  },
  // lib/hydrate-after-parse.ts stands in for Next's bootstrap in the browser bundle.
  webpack(cfg, { isServer, webpack }) {
    if (!isServer) {
      cfg.plugins.push(new webpack.NormalModuleReplacementPlugin(/^\.\/app-bootstrap$/, (r: { context: string; request: string }) => {
        if (/next[\\/]dist[\\/](esm[\\/])?client$/.test(r.context)) r.request = path.join(__dirname, "lib/hydrate-after-parse.ts");
      }));
    }
    return cfg;
  },
  async headers() {
    return [{ source: "/:path*", headers: securityHeaders }];
  },
};

export default config;
