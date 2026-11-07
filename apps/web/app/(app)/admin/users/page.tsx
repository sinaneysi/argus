import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { AdminsOnly } from "@/components/Agent";
import { Banners } from "@/components/Section";
import { SettingsTabs } from "@/components/Settings";
import { UsersBody } from "@/components/UsersBody";
import { plural } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AdminUsers } from "@/lib/types";

export const metadata: Metadata = { title: "Users and access" };

export default async function UsersPage() {
  const [r, csrf, flash] = await Promise.all([apiGet<AdminUsers>("/api/v1/admin/users"), csrfToken(), readFlash()]);
  if (r.status === 403) return <><header className="phead"><div><h1>Users and access</h1><HelpQ p="/admin/users" /></div></header><AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/admin/users answered ${r.status}`);
  return (
    <>
      <header className="phead"><div><h1>Users and access</h1><HelpQ p="/admin/users" /><p className="psub">{plural(r.data.rows.length, "account")}</p></div></header>
      <SettingsTabs cur="/admin/users" />
      <div className="stack"><Banners flash={flash} /><UsersBody d={r.data} csrf={csrf} /></div>
    </>
  );
}
