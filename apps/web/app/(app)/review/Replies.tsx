import { HelpQ } from "@/components/HelpQ";
import { replyAct } from "./actions";
import "./queue.css";
import { AdminsOnly } from "@/components/Agent";
import { Reload } from "@/components/Infra";
import { Banners, Crumb, Hidden } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Replies } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

const HERE = "/review?kind=replies&mode=full";

/** Review's full list of chat requests: choose the assignee, then create the task or discard the request. */
export async function RepliesFull({ params }: { params: Record<string, string> }) {
  const [r, csrf, flash] = await Promise.all([apiGet<Replies>("/api/v1/replies" + qs(params)), csrfToken(), readFlash()]);
  const head = <header className="phead"><div><Crumb href="/review?kind=replies" label="Review" /><h1>Chat requests</h1><HelpQ p="/review" /><p className="psub">Not in the tracker yet. Answer in the DM or here.</p></div></header>;
  if (r.status === 403) return <>{head}<AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/replies answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Reload r={d.refresh} />
      {head}
      <Banners list={d.banners} flash={flash} />
      <div className="stack"><PlainCard as="section" className="l-card" aria-labelledby="mx-h"><div className="card-h pad"><h2 id="mx-h">Waiting for your OK <span className="ncount">{d.rows.length}</span></h2></div>
        {d.rows.length ? <ul className="rows">{d.rows.map((w) => (
          <li key={w.pid} className="fl-row"><div className="fl-main">
            <div className="fl-t"><b dir="auto">{w.title}</b></div>
            <div className="fl-m"><span>Requester: {w.requester}</span><span>forwarded {w.forwarded} ago</span><span>expires in {w.hours} h</span><span className="mono">{w.project} · Task</span></div>
            {w.summary ? <p className="small" dir="auto">{w.summary}</p> : null}
            <form action={replyAct} className="fl-in"><Hidden csrf={csrf} pid={w.pid} return={HERE} />
              <label className="sr" htmlFor={`mx-${w.pid}`}>Assignee</label>
              <select id={`mx-${w.pid}`} name="assignee" defaultValue={w.assignee}>{w.choices.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}</select>
              <Button variant="outline" size="sm" name="action" value="create">Create task</Button><Button variant="destructive" size="sm" name="action" value="discard">Discard</Button></form>
          </div></li>
        ))}</ul> : <p className="empty pad">Nothing waits here. <a href="/review?kind=replies">Back to the review queue</a></p>}
      </PlainCard></div>
    </>
  );
}
