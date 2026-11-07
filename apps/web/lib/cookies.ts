// Parsing the API's Set-Cookie headers so a server action can hand them to the browser unchanged.

export type ParsedCookie = {
  name: string;
  value: string;
  path?: string;
  maxAge?: number;
  httpOnly: boolean;
  secure: boolean;
  sameSite?: "strict" | "lax" | "none";
};

export function parseSetCookie(header: string): ParsedCookie | null {
  const parts = header.split(";").map((p) => p.trim());
  const first = parts.shift() ?? "";
  const eq = first.indexOf("=");
  if (eq <= 0) return null;
  const name = first.slice(0, eq).trim();
  if (!/^[A-Za-z0-9_\-.]+$/.test(name)) return null;
  const c: ParsedCookie = { name, value: first.slice(eq + 1).trim(), httpOnly: false, secure: false };
  for (const p of parts) {
    const [k = "", v = ""] = p.split("=", 2).map((x) => x.trim());
    switch (k.toLowerCase()) {
      case "path":
        c.path = v;
        break;
      case "max-age": {
        const n = Number.parseInt(v, 10);
        if (Number.isFinite(n)) c.maxAge = n;
        break;
      }
      case "httponly":
        c.httpOnly = true;
        break;
      case "secure":
        c.secure = true;
        break;
      case "samesite": {
        const s = v.toLowerCase();
        if (s === "strict" || s === "lax" || s === "none") c.sameSite = s;
        break;
      }
    }
  }
  return c;
}
