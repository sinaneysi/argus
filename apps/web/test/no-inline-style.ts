// The nonce CSP blocks a style="" attribute and a <style> element in server HTML. An inline style set after hydration
// goes through CSSOM and is allowed, so only the server HTML needs this check. Every page test's html() helper calls it.
export function noInlineStyle(html: string): string {
  const hit = html.match(/<[a-zA-Z][^>]*\sstyle=[^>]*>|<style[\s>]/);
  if (hit) throw new Error("server HTML breaks the CSP: " + hit[0].slice(0, 200));
  return html;
}
