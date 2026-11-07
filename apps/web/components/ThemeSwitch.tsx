import { cookies } from "next/headers";
import { setTheme } from "@/app/actions";
import { THEME_COOKIE } from "@/lib/names";
import { csrfToken, currentUrl } from "@/lib/server";
import { Icon } from "./Icon";
import { ThemeForm } from "./Instant";

const THEMES = [
  { v: "auto", label: "Auto", icon: "monitor", title: "Follow the system" },
  { v: "light", label: "Light", icon: "sun", title: "Light" },
  { v: "dark", label: "Dark", icon: "moon", title: "Dark" },
];

/** Auto / Light / Dark as one form of three submit buttons: works without client script; with it the page changes at once. */
export async function ThemeSwitch({ compact = false }: { compact?: boolean }) {
  const t = (await cookies()).get(THEME_COOKIE)?.value;
  const cur = t === "light" || t === "dark" ? t : "auto";
  const [csrf, back] = await Promise.all([csrfToken(), currentUrl()]);
  const themes = THEMES.map((x) => ({ v: x.v, label: x.label, title: x.title, icon: <Icon name={x.icon} size={14} /> }));
  return <ThemeForm cur={cur} csrf={csrf} back={back} compact={compact} themes={themes} action={setTheme} />;
}
