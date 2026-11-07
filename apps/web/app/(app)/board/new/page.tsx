import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { NewTaskForm } from "@/components/NewTaskForm";
import { Banners, Crumb, pick, type Search } from "@/components/Section";
import "@/components/work.css";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Banner, NewTask } from "@/lib/types";

export const metadata: Metadata = { title: "New task" };

export default async function NewTaskPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["src", "id", "return"]);
  const [r, csrf, flash] = await Promise.all([apiGet<NewTask>("/api/v1/board/new" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/board/new answered ${r.status}`);
  const d = r.data;
  if (d.exists) {
    return (
      <>
        <header className="phead"><div><Crumb href="/board" label="Board" /><h1>New task</h1><HelpQ p="/board" /></div></header>
        <Banners list={[{ kind: "warn", text: "", segs: [{ text: "A task for this source exists already: " }, d.exists] }]} />
      </>
    );
  }
  const list: Banner[] = [];
  if (d.blocked) list.push({ kind: "warn", text: d.blocked });
  if (d.values.env === "release") list.push({ kind: "info", text: "Production: this task is a proposal for a human. Argus never runs a production change." });
  return (
    <>
      <header className="phead"><div><Crumb href="/board" label="Board" /><h1>New task</h1><HelpQ p="/board" /><p className="status-line">{d.sub}</p></div></header>
      <Banners list={list} flash={flash} />
      <NewTaskForm d={d} csrf={csrf} />
    </>
  );
}
