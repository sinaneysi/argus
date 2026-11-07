import type { Metadata } from "next";
import { currentOf } from "@/components/Sidebar";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";
import { apiGet, currentUrl } from "@/lib/server";
import type { Nav } from "@/lib/types";

export const metadata: Metadata = { title: "Not found" };

// Inside the shell: a dead link from a chat or a ticket keeps the sidebar and search, and leads back to the list it came from.
export default async function NotFound() {
  const [url, nav] = await Promise.all([currentUrl(), apiGet<Nav>("/api/v1/nav")]);
  const base = "/" + (url.split("?")[0]?.split("/")[1] ?? "");
  const cur = nav.status === 200 ? currentOf(nav.data, base) : {};
  return (
    <>
      <header className="phead"><div><h1>Not found</h1><p className="psub">Nothing is at this address. The record may have been merged or removed, or the link may be wrong.</p></div></header>
      <PlainCard as="section" className="flex flex-wrap gap-2">
        {cur.label ? <a data-slot="button" className={buttonVariants()} href={base}>Back to {cur.label}</a> : null}
        <Button type="button" variant="outline" data-cmdk-open>Search</Button>
        <a data-slot="button" className={buttonVariants({ variant: "ghost" })} href="/today">Go to Today</a>
      </PlainCard>
    </>
  );
}
