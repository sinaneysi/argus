import { appBootstrap as start } from "next/dist/client/app-bootstrap";

// Next's scripts are async, so with a warm cache they can run while the end of the HTML is still on the wire. React
// then hydrates a half-parsed document and throws error 418 (reproduced by holding back the last 5% of a page). Waiting
// for DOMContentLoaded removes that race. Every page is server rendered and works before hydration, so nothing is lost.
export function appBootstrap(hydrate: () => void): void {
  start(() => {
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", () => hydrate(), { once: true });
    else hydrate();
  });
}
