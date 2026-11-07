"use client";

import type { ActionCommand, GoCommand } from "@/lib/shortcuts";
import { Keys } from "./Keys";
import { Button } from "./ui/button";
import { Dialog, DialogClose, DialogContent, DialogTitle } from "./ui/dialog";

/** The shortcut list. Loaded on the first "?". */
export default function ShortcutHelp({ open, onOpen, go, actions, singleKeys }: {
  open: boolean; onOpen: (on: boolean) => void; go: GoCommand[]; actions: ActionCommand[]; singleKeys: boolean;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpen}>
      <DialogContent className="top-1/2 max-h-[calc(100dvh-2rem)] max-w-[780px] -translate-y-1/2 overflow-auto p-6">
        <div className="flex items-center justify-between gap-3">
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogClose render={<Button variant="ghost" size="sm" />}>Close</DialogClose>
        </div>
        {singleKeys ? null : <p className="mt-1 text-muted-foreground">Single-key shortcuts are off. Ctrl K still works.</p>}
        <div className="kh-cols">
          <section>
            <h3>Anywhere</h3>
            <ul>
              <li><span>Open the command palette</span><Keys k="Ctrl K" /></li>
              {actions.filter((a) => a.keys).map((a) => <li key={a.id}><span>{a.label}</span><Keys k={a.keys!} /></li>)}
              <li><span>Next or previous row</span><Keys k="j k" /></li>
              <li><span>Open the marked row</span><Keys k="Enter" /></li>
            </ul>
          </section>
          <section>
            <h3>Go to</h3>
            <ul>{go.filter((c) => c.keys).map((c) => <li key={c.href}><span>{c.label}</span><Keys k={c.keys!} /></li>)}</ul>
          </section>
        </div>
      </DialogContent>
    </Dialog>
  );
}
