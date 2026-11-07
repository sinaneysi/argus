"use client";

import { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import { cn } from "cn";

// Base UI's Dialog: focus trap, Esc, outside click and scroll lock. Client only, and loaded with what opens it, so a page
// that never opens a dialog sends none of this. Nothing depends on a dialog without JavaScript (DESIGN.md 3.24).
const fade = "transition-opacity duration-(--t-overlay) ease-harbor data-ending-style:opacity-0 data-starting-style:opacity-0";

function Dialog(props: DialogPrimitive.Root.Props) {
  return <DialogPrimitive.Root {...props} />;
}

function DialogContent({ className, children, ...props }: DialogPrimitive.Popup.Props) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Backdrop data-slot="dialog-overlay" className={cn("fixed inset-0 z-40 bg-[color-mix(in_srgb,var(--text)_45%,transparent)]", fade)} />
      <DialogPrimitive.Popup
        data-slot="dialog-content"
        className={cn("fixed top-[14vh] left-1/2 z-50 w-[calc(100%-2rem)] max-w-2xl -translate-x-1/2 overflow-hidden rounded-(--r-overlay) border border-border bg-popover text-base text-popover-foreground shadow-overlay outline-none", fade, className)}
        {...props}
      >
        {children}
      </DialogPrimitive.Popup>
    </DialogPrimitive.Portal>
  );
}

function DialogTitle({ className, ...props }: DialogPrimitive.Title.Props) {
  return <DialogPrimitive.Title data-slot="dialog-title" className={cn("text-lg font-semibold", className)} {...props} />;
}

const DialogClose = DialogPrimitive.Close;

export { Dialog, DialogContent, DialogTitle, DialogClose };
