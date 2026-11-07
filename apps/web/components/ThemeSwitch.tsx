import { cookies } from "next/headers";
import { setTheme } from "@/app/actions";
import { THEME_COOKIE } from "@/lib/names";
import { csrfToken, currentUrl } from "@/lib/server";
import { Icon } from "./Icon";

const THEMES = [
  { v: "auto", label: "Auto", icon: "monitor", title: "Follow the system" },
  { v: "light", label: "Light", icon: "sun", title: "Light" },
  { v: "dark", label: "Dark", icon: "moon", title: "Dark" },
];

/** Auto / Light / Dark as one form of three submit buttons: works without client script. */
export async function ThemeSwitch({ compact = false }: { compact?: boolean }) {
  const t = (await cookies()).get(THEME_COOKIE)?.value;
  const cur = t === "light" || t === "dark" ? t : "auto";
  const [csrf, back] = await Promise.all([csrfToken(), currentUrl()]);
  return (
    <form action={setTheme} className={compact ? "theme compact" : "theme"} aria-label="Theme">
      <input type="hidden" name="csrf" value={csrf} />
      <input type="hidden" name="back" value={back} />
      {THEMES.map((x) => (
        <button key={x.v} name="theme" value={x.v} aria-pressed={cur === x.v} title={x.title} className={cur === x.v ? "on" : undefined}>
          <Icon name={x.icon} size={14} />
          <span className={compact ? "sr" : undefined}>{x.label}</span>
        </button>
      ))}
    </form>
  );
}
