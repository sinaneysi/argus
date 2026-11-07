import { notFound } from "next/navigation";

// An address no page answers: render not-found.tsx inside the shell, not the bare root page.
export default function Missing() {
  notFound();
}
