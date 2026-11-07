import { Fragment } from "react";

// A report's markdown as elements, the subset the Go renderer (md.go) writes: headings, lists, fences, tables, inline
// code, bold and https links. Text stays text, so nothing in a report can become markup.

const INLINE = /`([^`]+)`|\*\*([^*]+)\*\*|(https?:\/\/[^\s<&]+)/g;
// Help's own files also link inside the app with [label](/path) (reHelpLink in help.go), and Outline pages with
// [label](https://url) (outlineLinkRe); reports never do.
const INLINE_LOCAL = /`([^`]+)`|\*\*([^*]+)\*\*|(https?:\/\/[^\s<&]+)|\[([^[\]<>]+)\]\((\/(?:[A-Za-z0-9_.?=#&;%-][A-Za-z0-9/_.?=#&;%-]*)?|https:\/\/[^\s()<>"]{1,500})\)/g;
const HEAD = /^(#{1,4})\s+(.*)/;
const ITEM = /^\s*(?:[-*]|\d+\.)\s+(.*)/;
const FENCE = /^ {0,3}(`{3,}|~{3,})/;
const TABLE_SEP = /^\s*\|?(\s*:?-{3,}:?\s*\|)+\s*(:?-{3,}:?\s*)?$/;
const VERDICT = /^(?:\*\*)?(proven|probable|unproven|not proven)(?:\*\*)?(.*)$/i;

/** Inline code, bold and links in one line; `codeOnly` is the fix cards' rule (backticks only). */
export function Inline({ text, codeOnly, local }: { text: string; codeOnly?: boolean; local?: boolean }) {
  const out: React.ReactNode[] = [];
  let at = 0;
  for (const m of text.matchAll(local ? INLINE_LOCAL : INLINE)) {
    if (codeOnly && m[1] === undefined) continue;
    if (m.index > at) out.push(text.slice(at, m.index));
    if (m[1] !== undefined) out.push(<code key={m.index}>{m[1]}</code>);
    else if (m[2] !== undefined) out.push(<strong key={m.index}>{m[2]}</strong>);
    else if (m[4] !== undefined) out.push(<a key={m.index} href={m[5]} rel={m[5]?.startsWith("/") ? undefined : "noopener noreferrer"}>{m[4]}</a>);
    else out.push(<a key={m.index} href={m[3]} rel="noopener noreferrer">{m[3]}</a>);
    at = m.index + m[0].length;
  }
  if (at < text.length) out.push(text.slice(at));
  return <>{out}</>;
}

function cells(raw: string): string[] {
  const s = raw.trim().replace(/^\|+|\|+$/g, "");
  const out: string[] = [];
  let cur = "";
  let code = false;
  let prev = "";
  for (const ch of s) {
    if (ch === "`") code = !code;
    if (ch === "|" && !code && prev !== "\\") {
      out.push(cur);
      cur = "";
    } else if (ch === "|" && prev === "\\") cur = cur.slice(0, -1) + ch;
    else cur += ch;
    prev = ch;
  }
  out.push(cur);
  return out.map((c) => c.trim());
}

function Cell({ text, local }: { text: string; local?: boolean }) {
  const v = VERDICT.exec(text);
  if (!v) return <Inline text={text} local={local} />;
  const word = v[1]!.toLowerCase();
  return <><span className={`pill ${word === "proven" ? "ok" : "warn"}`}>{word}</span><Inline text={v[2] ?? ""} local={local} /></>;
}

export function Markdown({ text, ids, local }: { text: string; ids?: boolean; local?: boolean }) {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const out: React.ReactNode[] = [];
  let para: string[] = [];
  let list: string[] = [];
  let hn = 0;
  let key = 0;
  const flushPara = () => {
    if (para.length) out.push(<p key={key++} dir="auto"><Inline text={para.join(" ")} local={local} /></p>);
    para = [];
  };
  const flushList = () => {
    if (list.length) out.push(<ul key={key++}>{list.map((x, i) => <li key={i} dir="auto"><Inline text={x} local={local} /></li>)}</ul>);
    list = [];
  };
  const flush = () => {
    flushPara();
    flushList();
  };
  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i]!;
    const fence = FENCE.exec(raw);
    if (fence) {
      flush();
      const mark = fence[1]!;
      const code: string[] = [];
      for (i++; i < lines.length; i++) {
        const c = FENCE.exec(lines[i]!);
        if (c && c[1]![0] === mark[0] && c[1]!.length >= mark.length && lines[i]!.trim() === c[1]) break;
        code.push(lines[i]!);
      }
      out.push(<pre key={key++} className="code"><code>{code.join("\n")}</code></pre>);
      continue;
    }
    if (raw.trim().startsWith("|") && TABLE_SEP.test(lines[i + 1] ?? "")) {
      flush();
      const head = cells(raw);
      const right = cells(lines[i + 1]!).map((c) => c.endsWith(":") && !c.startsWith(":"));
      const body: string[][] = [];
      for (i += 2; i < lines.length && lines[i]!.trim().startsWith("|"); i++) body.push(cells(lines[i]!));
      i--;
      out.push(
        <div key={key++} className="tablewrap"><table>
          <thead><tr>{head.map((c, n) => <th key={n} className={right[n] ? "num" : undefined}><Inline text={c} local={local} /></th>)}</tr></thead>
          <tbody>{body.map((r, x) => <tr key={x}>{head.map((_, n) => <td key={n} className={right[n] ? "num" : undefined}><Cell text={r[n] ?? ""} local={local} /></td>)}</tr>)}</tbody>
        </table></div>,
      );
      continue;
    }
    const head = HEAD.exec(raw);
    const item = ITEM.exec(raw);
    if (head || !raw.trim()) {
      flush();
      if (head) {
        hn++;
        const Tag = `h${Math.min(head[1]!.length + 1, 5)}` as "h2";
        out.push(<Tag key={key++} id={ids ? `s${hn}` : undefined} dir="auto"><Inline text={head[2]!} local={local} /></Tag>);
      }
    } else if (item) {
      flushPara();
      list.push(item[1]!);
    } else if (list.length && /^[ \t]/.test(raw)) {
      list[list.length - 1] += " " + raw.trim();
    } else {
      flushList();
      para.push(raw.trim());
    }
  }
  flush();
  return <Fragment>{out}</Fragment>;
}
