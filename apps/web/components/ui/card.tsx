import { cn } from "cn";

// `as` keeps the semantics of a page's <section>, <figure> or <details> on the card.
function Card({ as = "div", className, size = "default", ...props }: Omit<React.ComponentProps<"div">, "ref"> & { as?: "div" | "section" | "figure" | "details"; size?: "default" | "sm" }) {
  const Tag = as as "div";
  return (
    <Tag
      data-slot="card"
      data-size={size}
      className={cn(
        "group/card ui-card",
        className,
      )}
      {...props}
    />
  );
}

/** Like .card: padding on all sides, no [data-slot] reset, so pages built on .card-h and .card-sub keep their spacing. */
function PlainCard({ as = "div", className, ...props }: React.HTMLAttributes<HTMLElement> & { as?: "div" | "section" | "details" | "article" | "form" | "aside" | "p"; action?: string | ((fd: FormData) => void | Promise<void>); method?: string; autoComplete?: string; open?: boolean }) {
  const Tag = as as "form";
  return <Tag className={cn("plain-card", className)} {...props} />;
}

function CardHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-header"
      className={cn(
        "grid auto-rows-min items-start gap-1 px-(--card-spacing) has-data-[slot=card-action]:grid-cols-[1fr_auto] [.border-b]:pb-(--card-spacing)",
        className,
      )}
      {...props}
    />
  );
}

function CardTitle({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="card-title" className={cn("text-lg font-semibold group-data-[size=sm]/card:text-base", className)} {...props} />;
}

function CardDescription({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="card-description" className={cn("text-muted-foreground", className)} {...props} />;
}

function CardAction({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="card-action" className={cn("col-start-2 row-span-2 row-start-1 self-start justify-self-end", className)} {...props} />;
}

function CardContent({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="card-content" className={cn("px-(--card-spacing)", className)} {...props} />;
}

function CardFooter({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="card-footer" className={cn("flex items-center border-t px-(--card-spacing) py-3", className)} {...props} />;
}

/** The common card: a sentence title, an optional action on its row, then the body. */
function TitledCard({ id, titleId, title, action, className, bodyClassName, children }: { id?: string; titleId: string; title: React.ReactNode; action?: React.ReactNode; className?: string; bodyClassName?: string; children: React.ReactNode }) {
  return (
    <Card as="section" id={id} className={cn("gap-0", className)} aria-labelledby={titleId}>
      <CardHeader className="items-center">
        <CardTitle><h2 id={titleId} className="flex items-center gap-2 [&_.muted]:text-base [&_.muted]:font-normal">{title}</h2></CardTitle>
        {action ? <CardAction className="row-span-1 self-center">{action}</CardAction> : null}
      </CardHeader>
      <CardContent className={cn("[&>.empty:first-child]:mt-2.5 [&>.md:first-child]:mt-2.5", bodyClassName)}>{children}</CardContent>
    </Card>
  );
}

export { TitledCard, PlainCard, Card, CardHeader, CardFooter, CardTitle, CardAction, CardDescription, CardContent };
