import type { Metadata, Viewport } from "next";
import { CSPProvider } from "@base-ui/react/csp-provider";
import { cookies } from "next/headers";
import "./tailwind.css";
import "@argus/design/tokens.css";
import "./base.css";
import { THEME_COOKIE } from "@/lib/names";

export const metadata: Metadata = {
  title: { default: "Argus", template: "%s · Argus" },
  icons: {
    icon: [{ url: "/static/favicon.svg", type: "image/svg+xml" }, { url: "/static/favicon-32.png", sizes: "32x32", type: "image/png" }],
    apple: "/static/apple-touch-icon.png",
  },
  robots: { index: false, follow: false },
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#dce8f6" },
    { media: "(prefers-color-scheme: dark)", color: "#0b1220" },
  ],
};

// Every page renders per request (the data is the visitor's).
export const dynamic = "force-dynamic";

export default async function RootLayout({ children }: { children: React.ReactNode }) {
  const t = (await cookies()).get(THEME_COOKIE)?.value;
  const theme = t === "light" || t === "dark" ? t : undefined;
  return (
    <html lang="en" data-theme={theme}>
      <body>
        <a className="skip" href="#main">Skip to main content</a>
        {/* In the root layout: a page reached by client navigation keeps the layouts it finds, so a provider any lower is
            missing there and Base UI's <style> is blocked by the CSP. */}
        <CSPProvider disableStyleElements>{children}</CSPProvider>
      </body>
    </html>
  );
}
