import { cn } from "cn";

// Native controls on purpose: a form posts and reads the same with or without JavaScript, and Base UI's Select and Field
// write style attributes on the server (DESIGN.md 3.24). The old pages' 16px phone rule in base.css still applies.
const control =
  "w-full min-w-0 rounded-lg border border-input bg-card text-base font-normal text-foreground transition-colors duration-150 ease-harbor placeholder:text-muted-foreground hover:border-muted-foreground pointer-coarse:min-h-11 disabled:cursor-not-allowed disabled:bg-muted disabled:opacity-50 aria-invalid:border-destructive";

function Input({ className, ...props }: React.ComponentProps<"input">) {
  return <input data-slot="input" className={cn(control, "h-(--control) px-2.5", className)} {...props} />;
}

function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return <textarea data-slot="textarea" className={cn(control, "field-sizing-content min-h-16 px-3 py-2", className)} {...props} />;
}

function Select({ className, ...props }: React.ComponentProps<"select">) {
  return <select data-slot="select" className={cn(control, "h-(--control) w-fit px-2.5", className)} {...props} />;
}

function Label({ className, ...props }: React.ComponentProps<"label">) {
  return <label data-slot="label" className={cn("flex items-center gap-2 text-sm font-medium text-ink-2", className)} {...props} />;
}

/** A control under its label, like .field: the words above, the control filling the width. */
function Field({ label, className, children }: { label: React.ReactNode; className?: string; children: React.ReactNode }) {
  return <Label className={cn("grid gap-1.5", className)}><span>{label}</span>{children}</Label>;
}

export { Input, Textarea, Select, Label, Field };
