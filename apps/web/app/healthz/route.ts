// The container health check: the Next server answers (it does not ask the API, which has its own check).
export const dynamic = "force-dynamic";

export function GET() {
  return new Response("ok\n", { headers: { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store" } });
}
