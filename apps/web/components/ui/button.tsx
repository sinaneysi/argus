import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";

// A plain <button>, not Base UI's: it renders on the server, sends no JavaScript and works in a form without it.
// For a link that looks like a button: <a data-slot="button" className={buttonVariants()}>.
// The base styles are .ui-btn in tailwind.css: a button ships one short class, not this list twice per button.
// Sizes and borders match .btn in base.css, so a migrated page and an old page look the same.
// No focus classes: base.css's :focus-visible outline is unlayered and serves every control the same way.
const buttonVariants = cva(
  "ui-btn",
  {
    variants: {
      variant: {
        default: "border-primary bg-primary font-semibold text-primary-foreground hover:border-primary-hover hover:bg-primary-hover",
        outline: "border-border bg-card text-foreground hover:bg-accent",
        secondary: "bg-secondary text-secondary-foreground hover:bg-accent",
        ghost: "text-ink-2 hover:bg-accent hover:text-foreground",
        destructive: "border-destructive bg-card text-destructive hover:bg-accent",
        "destructive-ghost": "text-destructive hover:bg-accent",
        link: "text-primary underline-offset-4 hover:underline",
      },
      size: {
        default: "h-(--control) gap-1.5 px-[18px]",
        sm: "h-(--control-sm) gap-1.5 px-3.5 text-sm",
        lg: "h-(--control-lg) gap-1.5 px-[22px]",
        icon: "size-(--control)",
        "icon-sm": "size-(--control-sm)",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

function Button({ className, variant, size, ...props }: React.ComponentProps<"button"> & VariantProps<typeof buttonVariants>) {
  return <button data-slot="button" className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}

export { Button, buttonVariants };
