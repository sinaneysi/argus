import type { Metadata } from "next";
import { AdminsOnly } from "@/components/Agent";
import { HelpQ } from "@/components/HelpQ";
import { Pill } from "@/components/Pills";
import { Banners, Hidden, Refresh } from "@/components/Section";
import { SetForm } from "@/components/SetForm";
import { SettingsTabs } from "@/components/Settings";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AdminWritingStyle } from "@/lib/types";
import { settingsGo } from "../settings";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Writing style settings" };

const PAGE = "/admin/writing-style";
const head = <header className="phead"><div><h1>Writing style</h1><HelpQ p={PAGE} /><p className="psub">How Argus writes: task descriptions, plans, RCA reports, proposals and notices</p></div></header>;

/** Settings → Writing style: the style text every model run gets before its prompt, its editor and Reset to default. */
export default async function WritingStylePage() {
  const [r, csrf, flash] = await Promise.all([apiGet<AdminWritingStyle>("/api/v1/settings/writing-style"), csrfToken(), readFlash()]);
  if (r.status === 403) return <>{head}<AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/settings/writing-style answered ${r.status}`);
  const d = r.data;
  const kb = Math.round(d.max / 1024);
  return (
    <>
      <Refresh on={!!d.refresh} />
      {head}
      <SettingsTabs cur={PAGE} />
      <div className="stack">
        <Banners list={d.notices} flash={flash} />
        <PlainCard as="section" id="style-text" aria-labelledby="ws-h"><div className="card-h"><h2 id="ws-h">Style text</h2><Pill tone={d.custom ? "ok" : "neutral"}>{d.custom ? "Custom" : "Default"}</Pill></div>
          <p className="set-note">{d.line} Every model run gets this text before its prompt: task runs, RCA, proposals, ideas and chat. Tasks that Argus creates from incidents, RCA and CI/CD findings use the same layout.</p>
          <SetForm to="writing-style" page={`${PAGE}#style-text`} csrf={csrf} form="style" hidden={{ action: "save" }} submit="Save" stack
            note={`Applied within seconds. Plain text or Markdown, up to ${kb} KB.`}
            fields={[{ name: "text", label: "Style", type: "area", rows: 24, value: d.text, maxLength: d.max, required: true,
              hint: "Name a variable or a file, never a secret value: this text goes to the model with every prompt." }]} /></PlainCard>
        <PlainCard as="section" id="style-reset" aria-labelledby="ws-r"><div className="card-h"><h2 id="ws-r">Reset to default</h2></div>
          <p className="set-note">{d.custom ? "Delete the custom text and use the default style that ships with Argus." : "The default style is in use. Nothing to reset."}</p>
          {d.custom ? <details className="fold"><summary>Reset to default…</summary><form action={settingsGo} className="u-confirm"><Hidden csrf={csrf} _to="writing-style" action="reset" />
            <Button variant="destructive" size="sm">Reset to default</Button><span className="muted small">The custom text is deleted. Argus uses the default style again within seconds.</span></form></details> : null}</PlainCard>
      </div>
    </>
  );
}
