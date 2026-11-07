import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";

// Harbor status tints: a word first, the tint second (DESIGN.md 3.14).
const badgeVariants = cva(
  "inline-flex h-6 w-fit shrink-0 items-center justify-center gap-1 overflow-hidden rounded-4xl border border-transparent px-2 text-xs font-medium whitespace-nowrap tabular-nums [&>svg]:pointer-events-none [&>svg]:size-3",
  {
    variants: {
      variant: {
        neutral: "bg-secondary text-ink-2",
        outline: "border-border text-foreground",
        accent: "bg-soft text-soft-foreground",
        ok: "bg-ok text-ok-foreground",
        warn: "bg-warn text-warn-foreground",
        fail: "bg-fail text-fail-foreground",
        info: "bg-info text-info-foreground",
      },
    },
    defaultVariants: { variant: "neutral" },
  },
);

function Badge({ className, variant, ...props }: React.ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return <span data-slot="badge" className={cn(badgeVariants({ variant }), className)} {...props} />;
}

export { Badge, badgeVariants };
