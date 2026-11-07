"use client";

import { Button } from "@/components/ui/button";

// The last resort: a page whose data could not be read. The message is generic (no server details reach the browser).
export default function ErrorPage({ reset }: { error: Error; reset: () => void }) {
  return (
    <main id="main" className="solo">
      <div className="solo-box">
        <h1>Something went wrong</h1>
        <p className="muted">Argus could not read this page&apos;s data. Nothing was changed.</p>
        <p><Button variant="outline" type="button" onClick={() => reset()}>Try again</Button></p>
      </div>
    </main>
  );
}
