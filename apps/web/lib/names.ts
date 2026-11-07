// Cookie and header names shared with the Go API (apps/api/internal/app: api.go, auth.COOKIE, THEME_COOKIE, MFA_COOKIE).
export const CSRF_COOKIE = "argus_csrf";
export const FLASH_COOKIE = "argus_flash";
export const SESSION_COOKIE = "opsr_session";
export const THEME_COOKIE = "opsr_theme";
// Next only: the sidebar as an icon rail; not httpOnly, the button also sets it in the browser (components/Instant.tsx).
export const RAIL_COOKIE = "argus_sidebar";
