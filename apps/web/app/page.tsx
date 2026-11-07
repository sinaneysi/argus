import { redirect } from "next/navigation";

// Today is the landing page.
export default function Home() {
  redirect("/today");
}
