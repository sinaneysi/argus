import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { TitledCard, Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import { Table, TableBody, TableCaption, TableCell, TableFooter, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { noInlineStyle } from "./no-inline-style";

const html = (el: React.ReactElement) => noInlineStyle(renderToStaticMarkup(el));

describe("ui primitives", () => {
  it("are server components that import no Base UI", () => {
    const dir = join(__dirname, "..", "components", "ui");
    for (const f of ["badge", "button", "card", "field", "table"]) {
      const src = readFileSync(join(dir, f + ".tsx"), "utf8");
      expect(src, f).not.toContain('"use client"');
      expect(src, f).not.toContain("@base-ui");
    }
    expect(readdirSync(dir).length).toBeGreaterThanOrEqual(5);
  });

  it("loads dialogs and cmdk only through the lazy palette and shortcut list", () => {
    const root = join(__dirname, "..", "components");
    const eager = readFileSync(join(root, "Commands.tsx"), "utf8");
    expect(eager).not.toMatch(/from "[^"]*(ui\/dialog|ui\/command|cmdk)"/);
    expect(eager).toContain('dynamic(() => import("./Palette")');
    expect(eager).toContain('dynamic(() => import("./ShortcutHelp")');
  });

  it("button is a real button, keeps form attributes, and lets a class win", () => {
    const out = html(<Button type="submit" name="act" value="ack" variant="destructive" size="sm" disabled className="px-9">Go</Button>);
    expect(out).toMatch(/^<button data-slot="button"/);
    expect(out).toContain('type="submit"');
    expect(out).toContain('name="act"');
    expect(out).toContain("px-9");
    expect(out).not.toMatch(/\bpx-3\.5\b/);
    expect(buttonVariants({ variant: "outline" })).toContain("border-border");
  });

  it("badge has every status tint", () => {
    for (const variant of ["neutral", "outline", "accent", "ok", "warn", "fail", "info"] as const) {
      expect(html(<Badge variant={variant}>x</Badge>)).toContain('data-slot="badge"');
    }
    expect(html(<Badge variant="fail">Down</Badge>)).toContain("bg-fail");
  });

  it("card renders all its parts", () => {
    const out = html(
      <Card size="sm">
        <CardHeader><CardTitle>T</CardTitle><CardDescription>D</CardDescription><CardAction>A</CardAction></CardHeader>
        <CardContent>C</CardContent>
        <CardFooter>F</CardFooter>
      </Card>,
    );
    for (const s of ["card", "card-header", "card-title", "card-description", "card-action", "card-content", "card-footer"]) {
      expect(out).toContain(`data-slot="${s}"`);
    }
  });

  it("titled card links its title and keeps the action on the title row", () => {
    const out = html(<TitledCard id="x" titleId="x-h" title="T" action={<b>A</b>}>body</TitledCard>);
    expect(out).toMatch(/^<section data-slot="card"/);
    expect(out).toContain('aria-labelledby="x-h"');
    expect(out).toContain('<h2 id="x-h"');
    expect(out).toContain('data-slot="card-action"');
    expect(html(<TitledCard titleId="y" title="T">body</TitledCard>)).not.toContain('data-slot="card-action"');
  });

  it("table scrolls inside its container", () => {
    const out = html(
      <Table>
        <TableCaption>Hosts</TableCaption>
        <TableHeader><TableRow><TableHead scope="col">Name</TableHead></TableRow></TableHeader>
        <TableBody><TableRow><TableCell>a</TableCell></TableRow></TableBody>
        <TableFooter><TableRow><TableCell>b</TableCell></TableRow></TableFooter>
      </Table>,
    );
    expect(out).toMatch(/^<div data-slot="table-container"[^>]*overflow-x-auto/);
    expect(out).toContain('scope="col"');
  });

  it("fields are native controls that post by name", () => {
    expect(html(<Input name="q" defaultValue="x" />)).toMatch(/^<input data-slot="input".*name="q"/);
    expect(html(<Textarea name="t" />)).toMatch(/^<textarea data-slot="textarea"/);
    const sel = html(<Select name="s" defaultValue="b"><option value="a">A</option><option value="b">B</option></Select>);
    expect(sel).toMatch(/^<select data-slot="select".*name="s"/);
    expect(sel).toContain('value="b" selected');
    expect(html(<Label htmlFor="q">Q</Label>)).toContain('for="q"');
  });
});
