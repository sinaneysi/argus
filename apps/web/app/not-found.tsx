import Link from "next/link";
import { buttonVariants } from "@/components/ui/button";

export default function NotFound() {
  return (
    <main id="main" className="solo">
      <div className="solo-box">
        <h1>Not found</h1>
        <p className="muted">This page does not exist, or it moved.</p>
        <p><Link data-slot="button" className={buttonVariants({ variant: "outline" })} href="/today">Go to Today</Link></p>
      </div>
    </main>
  );
}
