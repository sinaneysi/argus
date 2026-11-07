/** A shortcut as key caps: "g t" is two keys. */
export function Keys({ k }: { k: string }) {
  return <span className="keys">{k.split(" ").map((x) => <kbd key={x}>{x}</kbd>)}</span>;
}
