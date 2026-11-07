"use client";

import { Command as CommandPrimitive } from "cmdk";
import { cn } from "cn";

// cmdk puts a style prop on its hidden label, which the CSP refuses in server HTML. Use it only inside a dialog: that is
// rendered on the client, where React writes styles through the DOM (DESIGN.md 3.24).
function Command({ className, ...props }: React.ComponentProps<typeof CommandPrimitive>) {
  return <CommandPrimitive data-slot="command" className={cn("flex flex-col bg-popover text-popover-foreground", className)} {...props} />;
}

function CommandInput({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Input>) {
  return <CommandPrimitive.Input data-slot="command-input" className={cn("h-14 w-full border-b bg-transparent px-4 text-lg outline-none placeholder:text-muted-foreground", className)} {...props} />;
}

function CommandList({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.List>) {
  return <CommandPrimitive.List data-slot="command-list" className={cn("max-h-[min(56vh,480px)] scroll-py-2 overflow-x-hidden overflow-y-auto p-2 outline-none", className)} {...props} />;
}

function CommandGroup({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Group>) {
  return <CommandPrimitive.Group data-slot="command-group" className={cn("pb-1 **:[[cmdk-group-heading]]:px-2 **:[[cmdk-group-heading]]:py-1.5 **:[[cmdk-group-heading]]:text-sm **:[[cmdk-group-heading]]:text-muted-foreground", className)} {...props} />;
}

function CommandItem({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Item>) {
  return <CommandPrimitive.Item data-slot="command-item" className={cn("flex min-h-11 min-w-0 cursor-pointer items-center gap-3 rounded-lg px-3 py-2 select-none data-[selected=true]:bg-soft", className)} {...props} />;
}

export { Command, CommandInput, CommandList, CommandGroup, CommandItem };
