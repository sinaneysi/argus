# Argus design system: Harbor

Argus is a work tool. People open it to clear what needs them and to make production decisions they can defend, on a
bright monitor during the day and on call at night. Every screen is an app screen, not a landing page. This file is
the component contract every Argus frontend is built from; it names no framework. The product-wide UX standard
(principles, queue rules, actions and safety, acceptance checklist) is [`DESIGN_SYSTEM.md`](DESIGN_SYSTEM.md); this
file is how Argus implements it, and where the two disagree, this file wins.

| file | what |
|---|---|
| [`tokens.css`](tokens.css) | every token as a CSS custom property: colour pairs (`light-dark()`), type, space, density, shape, motion |
| [`scripts/contrast.mjs`](scripts/contrast.mjs) | the contrast gate: every documented pair in both themes (`node packages/design/scripts/contrast.mjs`) |
| [`brand/`](brand/) | the mark: `mark.svg` (light), `mark-dark.svg` (dark), `mark-512.png` |
| `static/fonts/` | IBM Plex Sans, JetBrains Mono, Vazirmatn (variable woff2, SIL OFL 1.1, licences beside them) |
| `static/favicon.svg`, `favicon-32.png`, `favicon.ico` (16/32/48), `apple-touch-icon.png` (180) | the favicon set, drawn from `brand/mark.svg` |

**Direction.** Harbor: a soft, roomy console. A pale sky ground; a floating sidebar and white cards on it; large
radii, pill buttons and generous rows. Every card is titled with a sentence and a one-line subtitle that says what it
shows, one ocean-blue accent marks action and selection, and a numbered "Needs you" list leads every landing page,
with a side column for incidents, checks and the Review inbox. The operator chose this direction and colourway on
2026-11-04 from a static comparison of five versions and five colourways.

**Mood:** calm, friendly, roomy, legible, assured.
**Not:** a NOC wall of red tiles; a neon or "hacker" dark theme; KPI tiles with icons and sparklines; violet
everywhere (the accent is rare and means "act" or "selected").

## 1. Principles

1. **The most urgent thing leads.** Every landing page opens on "Needs you", ranked by a documented order.
2. **Every card says what it shows.** A sentence title (18/600) and a muted subtitle with the window or caveat
   ("Failed check runs per hour · last 48 h · production only"). The method folds behind an (i) button.
3. **Colour carries meaning, never decoration.** The accent marks action and selection; status colours mark state;
   data colours follow "good or bad, not up or down". Colour is never the only carrier: a word and, on rows, a glyph.
4. **Figures are type.** Tabular numbers, a true minus (−), the unit in muted text. No icons on KPIs, no sparklines.
5. **Tables are rules and alignment.** No zebra, no header band, numbers right-aligned, identity column widest.
6. **Only real data.** No invented counters, charts, error budgets or SLOs: a chart exists only when a data source
   provides its numbers. Missing is "—", zero is "0".
7. **Server truth.** Search, filters, sort and pagination live in the URL; success is shown only from the server's
   result, including partial results.
8. **Quiet motion.** Feedback only (press, hover, disclosure, overlays); no entrances, no counters ticking.

## 2. Tokens

All values are custom properties on `:root` in [`tokens.css`](tokens.css). Each colour is one `light-dark(light, dark)`
pair: `color-scheme: light dark` follows the system ("Auto"); `<html data-theme="light|dark">` pins it (the theme
switch). Native controls, scrollbars and `<meta name="theme-color">` follow the same choice (`#dce8f6` light,
`#0b1220` dark). Feature code uses tokens only; a colour literal outside `tokens.css` is a bug. A new colour is a new
`light-dark()` pair and a new line in the contrast script.

### 2.1 Colour

| token | light | dark | use |
|---|---|---|---|
| `--bg` | `#dce8f6` | `#0b1220` | page ground: canvas, sidebar, top bar |
| `--surface` | `#ffffff` | `#131c2e` | cards, panels, tables, inputs |
| `--surface-2` | `#ebf2fa` | `#1d2840` | wells: code, neutral pills, tab track, icon circles |
| `--hover` | `#e6eef8` | `#1d2840` | row, nav and menu hover |
| `--border` | `#dfe8f3` | `#2a3650` | card edges and row rules (decorative, below 3:1 on purpose) |
| `--control-border` | `#66788f` | `#6e8099` | input, checkbox and radio boundaries (≥ 3:1) |
| `--text` | `#0f1b2d` | `#e8eff8` | primary ink: titles, figures, identity |
| `--text-2` | `#2f3f55` | `#c2cedd` | running text in rows, explanations |
| `--text-muted` | `#4f6178` | `#95a6bc` | labels, meta, axis ticks, subtitles (≥ 4.5:1 on every surface) |
| `--accent` | `#1459c7` | `#7fb2ff` | links, focus, selected text, the main data series |
| `--accent-strong` | `#1459c7` | `#7fb2ff` | the primary button fill (white text) |
| `--accent-hover` | `#0f49a8` | `#a1c7ff` | primary button hover |
| `--accent-contrast` | `#ffffff` | `#06182f` | text on `--accent-strong` |
| `--accent-soft` / `--accent-soft-fg` | `#dce9fc` / `#0f4aa6` | `#16304f` / `#a9cbff` | selected nav item, row, active filter |
| `--focus` | `#1459c7` | `#7fb2ff` | the focus ring |
| `--mark-bg` / `--mark-fg` | `#1459c7` / `#ffffff` | `#7fb2ff` / `#06182f` | the mark tile and its lines |
| `--ok-bg` / `--ok-fg` | `#e2f3e7` / `#1f6b3a` | `#14291c` / `#7ad79d` | healthy, done |
| `--warn-bg` / `--warn-fg` | `#fcf0d9` / `#835000` | `#2d2410` / `#ecbb60` | degrading, expiring, stale |
| `--fail-bg` / `--fail-fg` | `#fdeaea` / `#b42f33` | `#331719` / `#ff9191` | broken, at risk, production failure |
| `--info-bg` / `--info-fg` | `#e6eff8` / `#2f5a86` | `#17263a` / `#9cc1ea` | neutral notice |
| `--data-1` / `--data-1-soft` | `#1459c7` / `#9cc0f0` | `#7fb2ff` / `#2f5c99` | the main / current series; the rest of it |
| `--data-teal`, `--data-sand`, `--data-slate` | `#3f9a8e`, `#a8823a`, `#7a8fb6` | `#4fb3a5`, `#d6ad5f`, `#5f7499` | categories 2–4 |
| `--data-red` / `--data-red-soft` | `#d9776a` / `#f1c4be` | `#e0786c` / `#6b3631` | losses, failures (largest / minor) |
| `--data-green-soft` | `#bcdcc6` | `#2f5a40` | gains, credits, OK runs |
| `--data-grey` | `#e4ecf6` | `#253048` | neutral totals, free time, headroom |
| `--data-target` | `#0f1b2d` | `#e8eff8` | the 2px target or limit tick |

**Contrast (computed by `scripts/contrast.mjs`, WCAG 2.2 AA).** Text on every surface ≥ 13.5:1; `--text-muted`
≥ 4.95:1 (worst: light, on `--hover`); accent as text 6.70:1 light / 7.17:1 dark on `--surface`; white on the primary
fill 6.70:1 light / 5.33:1 dark (hover 8.32 / 6.55); selected `--accent-soft-fg` on `--accent-soft` 7.47 / 8.72;
status fg on its own bg 5.56–6.51 light / 7.54–8.69 dark; focus ring 6.21 / 7.69 on `--bg`; control boundaries
3.19–3.67; chart marks `--data-1`, teal, sand, slate, red ≥ 3:1 on `--surface` (light slate 3.26 and red 3.09 are the
lowest). Soft tints (`--data-1-soft`, `--data-red-soft`, `--data-green-soft`, `--data-grey`) are below 3:1 by design:
they only fill secondary segments that carry a printed value or a legend entry with the value.

**Colour rules.**
- The accent appears only on: the one primary button of a screen or panel, links, the selected nav item / row / tab,
  the focus ring, stepper progress, the main data series and the mark. Never on chrome backgrounds, headings or
  decoration. Target: ≤ about 3% of a screen's pixels; no gradients.
- Status colours only for state. fail = broken, at risk, production failure; warn = degrading, expiring, stale;
  ok = healthy, done; info = neutral notice. Every status cue pairs the colour with a word and, on rows, a glyph
  (fail: filled circle-x, warn: triangle, ok: check, info: i).
- Production is **named, not alarmed**: an ink `prod` pill with a 6px ink dot, never red.
- Severity is its own dimension: critical = filled `--fail-fg` pill with surface text; high = fail tint; medium =
  warn tint; low = neutral; unknown = neutral outline reading "unknown".

### 2.2 Type

IBM Plex Sans (variable, 100–700) for everything, chosen on 2026-11-04 after the operator rejected Onest as small and
ugly: it has clear `I l 1` and `0 O` for hosts and IDs, tabular figures by default, and a calm engineered voice. Vazirmatn
covers Arabic-script ranges at `size-adjust: 110%`, so Persian matches Plex in size (mixed Persian/Latin titles use
`unicode-bidi: plaintext`). JetBrains Mono only for hosts, IDs, SHAs, paths, job names, ISO timestamps inside
evidence, logs and code; never for labels, pills or headings. Self-hosted, `font-display: swap`, no external loading.
Tabular figures (`font-variant-numeric: tabular-nums`) on every figure, time, count, estimate and axis; a true minus
(−) and plus on signed changes; slashed zero (`font-feature-settings: "zero"`) in mono IDs.

| token | size / line | use |
|---|---|---|
| `--fs-xs` / `--lh-xs` | 13 / 18 | meta, pill text, axis ticks, table headers; the floor: nothing a person reads is smaller |
| `--fs-sm` / `--lh-sm` | 14 / 20 | table cells, secondary text, row meta, small buttons, filter pills |
| `--fs-md` / `--lh-md` | 16 / 24 | body, nav links, buttons, card subtitles, kanban card titles |
| `--fs-lg` / `--lh-lg` | 18 / 26 | card titles (600) |
| `--fs-xl` / `--lh-xl` | 22 / 30 | panel title, detail object name (600) |
| `--fs-2xl` / `--lh-2xl` | 32 / 40 | page title h1 (600, −0.02em; 26/32 on phones) |
| `--fs-fig` / `--lh-fig` | 32 / 40 | the headline figure (600, tabular) |
| `--fs-fig-sm` / `--lh-fig-sm` | 22 / 28 | sibling figures in stat tiles |
| `--fs-code` | 13 | JetBrains Mono everywhere (its x-height is taller, so 13 reads as 14) |

Weights: 400 body, subtitles, meta; 500 nav links, pill text, filter values, row titles in dense tables; 600 h1,
card titles, activity-row titles, figures, the active nav item. Sentence case everywhere. Headings use
`text-wrap: balance`. Body line height is 1.5. Running text stops at `--measure` 62ch: `ch` is the width of "0", so that is about 74 average
characters. Sizes live in tokens; a literal font size in feature CSS is a bug.

### 2.3 Space, grid, density

- 4px base: `--s1` 4, `--s2` 8, `--s3` 12, `--s4` 16, `--s5` 24, `--s6` 32, `--s7` 48 (also 20 and 40 where named).
- Page padding `--pad` 32 (24 at ≤ 1280px, 16 on phones). Grid gap `--gap` 24 (16 on phones). Card padding
  `--card-pad` 24 (20 in dense cards, 16 on phones). Card title → subtitle 4px; subtitle → content 20px; between card
  rows 24px.
- Canvas: fluid, left-aligned beside the 264px sidebar, stopping only at `--content-max` 2400px (the top bar stops with
  it). Blocks directly under the page are `--gap-block` 20 apart, whatever they are.
- **Width rule.** Queues, tables, boards and dashboards fill the canvas. Prose and forms keep `--measure`; on a wide canvas
  they gain columns, never longer lines: from 1500px page stacks of cards, Help sections, the page note and the task form
  go two-up; from 1700px the main column of a detail page does too (cards that hold a table keep the full width). The side
  rail of a detail page drops under the main column at 1280px and below.
- Density tokens: `--row` 48 (table row), `--row-compact` 40 (opt-in dense tables), `--row-queue` 60 (two-line
  queue row), `--row-activity` 64 (two-line activity row), `--th` 40 (table header), `--control` 40 (buttons, filter
  pills, inputs), `--control-sm` 34, `--control-lg` 44 (primary in panel action bars), `--nav-item` 38 (on a 40px
  pitch), `--pill` 22, `--topbar` 56. On `(pointer: coarse)` every target is at least `--target-coarse` 44px.

### 2.4 Shape and elevation

| token | value | use |
|---|---|---|
| `--r-chip` | 4px | kbd, kind tags, chips inside tooltips |
| `--r-control` | 8px | buttons, inputs, selects, nav items |
| `--r-filter` / `--r-inner` | 10px | filter pills, segmented tabs, banners, code blocks, row hover overhang |
| `--r-overlay` | 12px | menus, tooltips, the command palette |
| `--r-card` | 14px | cards and panels |
| `--r-pill` | 999px | status, impact and env pills, count badges |

Nesting: inner radius = outer − padding, minimum 4. Elevation 0: `--bg` (canvas). Elevation 1: `--surface`
with a 1px `--border`, no shadow (cards, panels, tables). Elevation 2, only what floats (tooltips, menus, the
command palette, the phone navigation sheet): `--surface` + 1px border + `--shadow-overlay`. A border or a shadow,
never both at rest.

### 2.5 Motion

| token | value | animates |
|---|---|---|
| `--t-fast` | 100ms | hover background and colour on rows, nav, pills (`ease`) |
| `--t` | 160ms | press `scale(.98)` on buttons and filter pills, sidebar chevron 90°, tooltip, disclosure |
| `--t-overlay` | 180ms | menus (opacity + 4px rise), phone sheet (slide from the left) |
| `--ease-out` | `cubic-bezier(.23, 1, .32, 1)` | everything that moves |

Tooltips wait 250ms on the first hover only and show instantly when moving between adjacent bars. Never: page or
card entrances, counters ticking, row reordering, keyboard-driven selection, charts drawing in. Name the property,
never `transition: all`. Hover styles live behind `@media (hover: hover) and (pointer: fine)`.
`prefers-reduced-motion: reduce` removes every transform and travel, keeps ≤ 100ms opacity and colour, shows tooltips
without delay or travel, and turns the pending spinner into a static "Working…" label.

### 2.6 Icons

Lucide (ISC licence), outline, 1.5px stroke: 16px in nav, buttons and filter pills; 18px inside 32px circles on
activity rows and findings. Self-hosted (one SVG sprite or inlined), `aria-hidden="true"`, `currentColor`. Circles
are neutral (`--surface-2` with a `--text-muted` icon) for event kinds; status-tinted only on "Needs you" findings,
where the tint states the status. No sparkle or AI glyphs; "Needs you" uses the Argus eye.

### 2.7 The mark

An eye whose upper lid peaks like an A (Argus, the watchman with a hundred eyes) on a 32px tile with an 8px radius.
Light: white 2.6px lids and iris on `#1459c7`. Dark: `#06182f` on `#7fb2ff`, so it keeps its
weight on the near-black ground. In the sidebar header it is 28px beside "Argus" (18/600) and the install name
(12px muted). The favicon set uses the light tile. Status never recolours the mark; never stretch, outline, add a
gradient or put it on a photo.

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="8" fill="#1459c7"/><path d="M6 18 16 8l10 10M6 18q10 9 20 0" fill="none" stroke="#fff" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/><circle cx="16" cy="17.5" r="3.2" fill="#fff"/></svg>
```

## 3. Components

Each component lists its anatomy, its states, the tokens it uses and its accessibility contract. "Link" means a real
`<a href>`; "form" means a real `<form>` that works without client script (progressive enhancement may improve it,
never replace it).

### 3.1 App shell
- **Anatomy:** grid `--side` 264px | main `1fr`. The shell has 12px padding and a 12px gap. The sidebar floats on `--surface`, radius 20, `--shadow-card`, and holds the search form; main on
  `--bg`. A 56px top bar over the main column. Content max `--content-max` (§2.3).
- **States:** groups collapsed or open, current page, counts, phone sheet open.
- **Phone (≤ 900px):** the sidebar becomes a sheet sliding from the left (`--t-overlay`), opened from a menu button
  in the top bar; Esc or a Close link returns focus to the menu button.
- **A11y:** a "Skip to main content" link first; `<nav aria-label="Main">`; `<main id="main" tabindex="-1">`.

### 3.2 Sidebar
- **Anatomy:** header (mark 28px + "Argus" + install name); six groups in order: Home, Incidents, Infrastructure,
  Delivery, Work, Agent. Each group is a disclosure: a 40px summary with a 16px chevron, an 18px group icon and a 14/600
  label in `--text-2`. Links: 38px, indented so the 16px icon sits under the group icon, 14/400 label. Size never carries
  the hierarchy: weight, icons and indent do. A page's icon differs from its group's. Footer: Help, Settings (admins), the theme switch (Auto / Light / Dark), the user row
  (28px initials avatar, name, role, sign-out).
- **States:** hover = `--hover` fill; current = `--accent-soft` fill, `--accent-soft-fg` label and icon, 14/600, radius 8,
  `aria-current="page"`; the current group keeps an `--accent-soft-fg` label with no fill; focus ring inset (offset −2px); the current page's group is open; a closed group shows the
  sum of its counts on its summary so a count is never hidden; viewers see no counts.
- **Counts:** right-aligned 13/600 tabular in a 20px pill; neutral (`--surface-2` / `--text-2`), fail tint only for
  open incidents and failing checks. Every count has a screen-reader phrase ("5 left today").

**Rail:** on desktop the sidebar folds to a 72px icon rail, with every group open and its summary hidden. A form sets the
`argus_sidebar` cookie, so it works without JavaScript, and the server renders the right width at once. With JavaScript
the button flips `data-rail` and writes the same cookie in the browser: no request, no jump. Search and the theme
switch hide on the rail; both stay in the palette. An alarm count becomes a red dot; the count stays for screen readers.

### 3.3 Top bar
- **Anatomy:** 56px, `--bg`, 1px bottom border, padding 0 `--pad`. Left: breadcrumb (13px muted links, "/"
  separators, the last item `--text` 500, `aria-current="page"`). Right: a search field that fills its container (icon, placeholder that
  names exactly what it searches, a GET form to the search route; a ⌘K palette is an optional enhancement and the
  form must work without it; show the ⌘K hint only when the palette exists), then the freshness pill. No bell: Argus
  has no notification centre.
- **Freshness pill:** "Data as of 09:42 · checks 09:40" from real data timestamps; a 6px dot: ok green; amber "stale"
  when a source is older than its window; red when a collector failed; each links to the source.
- **States:** search focus (2px ring), freshness ok / stale / failed; phone: the crumb collapses to the current page,
  search moves into the sheet, the menu button appears.

### 3.4 Page header
- **Anatomy:** 32px under the top bar. h1 32/40 600. Subtitle: one line, 16/24 `--text-muted`, phrased as the
  page's question ("What needs you now, and in what order?"). Optional status line, 13px `--text-2`, for the page's
  finding. Right slot: actions, at most one primary; the rest secondary, ghost or icon (36px). In-page views
  (CI/CD Failures / Stuck / Branches, Settings sections) sit under the subtitle as tabs.
- **States:** with or without actions; a long title wraps (balanced); phone: actions wrap under the title.

A page with a Help section has a small "?" link 8px after its title (`HelpQ`), however long the subtitle is: 20 px, muted, 44 px target on touch.

### 3.5 Filter pill bar
- **Anatomy:** one row, 8px gaps, wraps when narrow. Each filter is a 40px pill (radius 14, `--surface`, 1px border,
  padding 0 12): 16px icon, label 13px `--text-muted`, value 14/500 `--text`, chevron. It opens a menu of links or is
  a `<select>` in a GET form with an Apply button (which may hide when a script submits on change). Search is a pill
  field (160 to 280px, it gives way first). Reset is an icon-only ghost button (rotate-ccw), disabled while no filter is
  active, so the bar never shifts. The bar fits one line at 1440px: the result count is not shown in it (screen readers
  hear it as a status; the pager and the counts in each option carry it). The Review queue shows its total and time estimate in the header status line.
- **States:** default; active (non-default value) = `--accent-soft` fill, `--accent-soft-fg` value; menu open; focus;
  phone (wraps, search full width first). A disabled filter is never shown: omit it.
- **Rule:** only filters the server implements end to end.

### 3.6 Card / panel
- **Anatomy:** `--surface`, 1px `--border`, radius 20, padding 22, `--shadow-card`. Header: title 18/26 600 + optional 16px (i)
  disclosure that folds the method + a right slot (status pill or a link "Open in CI/CD →" in accent 14/500).
  Subtitle 16/24 muted, 4px under. Body 20px under. Optional footer: 1px top border, 12px padding, muted 13 note or
  "View all N →". Cards in one row align their tops (`align-items: start`); a short card keeps its height.
- **States:** default; working ("Updating…" pill in the header slot, no skeletons); empty; error; stale (warn pill
  "Stale · 3 h" in the header).
- **Rule:** no card inside a card. A table inside a card runs edge to edge with 24px side padding.

### 3.7 Needs-you card (signature)
- **Anatomy:** a card titled with the eye glyph (16px, accent) + "Needs you" + muted "· ranked by impact"; right: a
  neutral count pill "N items". Body: a 2-column grid (gap 32; 1 column ≤ 900px), at most 4 findings, then
  "N more in today's plan →". A finding: 32px status circle (fail / warn / info tint + 18px glyph) | title 18/26 600,
  a sentence that contains the number ("Backup set db-main failed 3 nights in a row") | a 2-line explanation 16/24
  `--text-2`, clamped | footer: impact pill(s) left, the action link right ("Backups →", accent 14/500).
- **Ranking:** the server's documented order (today's plan order or the review urgency order); the subtitle names it.
- **States:** 0 findings ("Nothing needs you. Next checks at 10:00." with an ok circle); 1 finding (full width);
  stale; viewer (the action reads "An admin's next step").
- **A11y:** a `<section aria-labelledby>`; findings are a list; the status is in the title's words, not only the tint.

### 3.8 Impact pill
24px, radius 999, padding 0 8, 13/500 tabular. Signed text: "−38% error budget", "+2 hosts back", "expires in 6 h".
fail tint for the bad direction, ok for the good one, warn for time pressure, neutral for scope ("3 prod hosts").
Optional 6px dot. Static only.

### 3.9 Stat tiles
- **Anatomy:** equal columns, 12px gap. Each tile is its own floating card: `--surface`, 1px `--border`, radius 20,
  `--shadow-card`, padding 14 18. Each: label 13 muted; the figure 28/34 600
  tabular (the page's answer) or 22/28 for siblings; optional unit 14 muted; optional delta as an impact pill with its
  window in words ("+3 since 08:00"). The whole tile is a link to its list (hover fill).
- **States:** zero printed as 0; missing as "—"; a stale value carries a warn dot; current filter = `--accent-soft`.
- **Rule:** no tile per KPI, no icons, no sparklines.

### 3.10 Activity and list rows
- **Anatomy:** 60px two-line rows inside a card: 32px neutral circle (kind icon) | title 16/24 600 | meta 14/20
  muted ("Pipeline 4412 · develop · Alex Doe") | right: date or age 13 muted tabular, optional pill. 1px rules start
  at the text column (they skip the circle).
- **States:** hover = `--hover` fill, radius 10, overhanging 8px; focus; new (6px accent dot before the title);
  truncated title (full text in `title`).
- **A11y:** when a row is a link, the whole row is one link with the title as its name; inner controls stay separate.

### 3.11 Queue rows (Review)
56px: 28px severity signal bars (`components/Severity.tsx`; 4 bars critical to 1 low, a kind with no severity shows none) | title 16/600 one line + meta 13 muted ("Task proposal · prod ·
from CI/CD") | right: a deadline impact pill when < 24 h ("expires in 6 h", warn), else muted "expires 9 Nov", and the
waiting age. Selected: `--accent-soft` fill + a 3px accent inset edge on the left + a visually hidden "in the panel",
`aria-current="true"`. Rows are links (`?sel=kind:id`).

### 3.12 Table
- **Anatomy:** inside a card, edge to edge with 24px side padding. Header 36px: 13/500 muted, sentence case, 1px
  rule under, a sort glyph on the active column only, numeric headers right-aligned. Rows 44px (36 compact), 1px rules,
  no zebra. The table card keeps a 6px inset and a hovered row has rounded ends. Identity column widest; numbers right-aligned tabular; IDs mono, truncated, with a copy affordance.
- **States:** hover; selected (checkbox) = `--accent-soft`; focus-within; empty; filtered empty; error row.
- **Bulk:** the bulk bar appears only while a row is selected (`:has(:checked)` or equivalent), as a sticky card
  footer with the count, the actions and "Clear"; a bulk decision goes through a confirm step with counts and
  consequences. Pagination footer "1–25 of 61" + Prev / Next.
- **Wide tables:** deliberate horizontal scroll with the first column pinned and the scrollbar shown; on phones, stack rows into labelled
  lines rather than hiding critical columns.

### 3.13 Stepper
- **Anatomy:** horizontal, equal columns; an `<ol>` with `aria-current="step"` on the current node. Node 24px circle:
  done = `--accent-strong` fill + white check; current = 2px accent ring + 8px accent dot; future = 1.5px
  `--control-border` ring. Connector 2px: accent between done nodes, `--border` after the current. Under each node:
  label 16/500 (current `--text` 600, future muted) and duration 13 muted tabular ("—" when not reached; current
  duration in `--warn-fg` when over the median). Header right: warn pill "9 h in stage" + muted "median 2 h".
- **Uses:** incidents (Detected · Triaged · Mitigating · Resolved · RCA), deploys (Build · Test · Stage · Approve ·
  Production), action approvals (Proposed · Plan reviewed · Approved · Running · Done).
- **States:** done; current; current overdue; failed (fail ring + x, the connector stops); skipped (dashed connector,
  "skipped" meta). Phone: vertical, nodes left, labels right. Each node has a hidden "done:" / "to do:" prefix.

### 3.14 Pills, tags and badges
- **Status pill:** 22px, radius 999, 13/500, 6px dot + word, the status tint pair.
- **Severity pill:** as the status pill with a glyph (critical filled `--fail-fg` / surface text, high fail tint, medium
  warn tint, low neutral, unknown neutral outline).
- **Env pill:** neutral `--surface-2` / `--text-2`; production = ink text 600 "prod" with an ink dot; "pre", "dev".
- **Kind tag:** radius 4, neutral, 13/500 ("Task proposal").
- **Count badge:** 18px pill, 13/600, neutral, or fail tint for open incidents.
- Chips never squeeze (`flex: none; white-space: nowrap`). As filters they become filter pills (§3.5).

### 3.15 Buttons
- **Anatomy:** height 36 (32 small, 40 in panel action bars), radius 8, 14/500 (600 for primary), padding 0 14, 16px
  icon with a 6px gap.
- **Variants:** primary = `--accent-strong` fill, white text, hover `--accent-hover`, one per screen or panel;
  secondary = `--surface`, 1px border, `--text`; ghost = no border, `--text-2`, hover fill; danger = `--fail-fg` text
  on surface with a border (danger-ghost inside folds), a filled fail button only inside a confirm step; icon = 36×36
  with `aria-label` and `title`.
- **States:** default, hover, active (`scale(.98)`), focus (2px ring, offset 2), disabled (45% opacity,
  `not-allowed`; still focusable when it explains why via `aria-describedby`), pending after a POST ("Creating…",
  disabled; the server's banner reports the result).
- **Copy:** verbs that name the outcome ("Create Jira issue", not "Accept").

### 3.16 Form controls
- **Anatomy:** inputs, selects, textareas 36px (textarea min 88), radius 8, `--surface` fill, 1px `--control-border`,
  14px text, placeholder muted. Label above 14/500 `--text-2`; hint below 13 muted; error below 13 `--fail-fg` with an
  icon, tied by `aria-describedby`, and `aria-invalid="true"` on the field. Checkbox and radio 16px drawn with
  `--control-border`, checked = `--accent-strong` fill + white mark. Switch 32×18 track.
- **States:** default, hover (border `--text-muted`), focus (2px ring offset 2), filled, error (border `--fail-fg`),
  disabled, read-only (`--surface-2` fill).

### 3.17 Tabs and chips
A segmented track: `--surface-2` fill, radius 10, padding 2. Tab 32px, 14/500 muted; selected = `--surface` fill,
`--text` 600, 1px border; a count after the label. Tabs are links (`?view=`) with `aria-current="page"`. The Review
kind chips use the same control with counts and scroll horizontally on phones. States: default, hover, selected,
focus.

### 3.18 Tooltip
A small surface card (elevation 2, radius 12, padding 10 12): label 13 muted, value 22/28 600 tabular, an optional
finding line 13 in a status fg ("Largest failure burst in 48 h"). No arrow. Chart tooltips work without script:
each bar group is focusable (`tabindex="0"`) and reveals its tip on `:hover` / `:focus-visible`; the same text is in
the SVG `<title>` and in the "Show data" table. Icon-button tooltips are the `title` attribute plus `aria-label`.

### 3.19 Banners
Full card width, radius 10, padding 12 16, the status tint pair with an icon; title 16/600 + one line. Dismiss is a
link that drops the query flag. A request banner after a POST reports the server's result, including partial results
("Created 3 of 4 issues; OPS-12 failed: Jira timeout"). `role="status"`, or `role="alert"` for failures. Variants:
ok, warn, fail, info.

### 3.20 States: empty, working, error, stale, permission
- **Empty:** inside the card, one short sentence in `--text-2` that states the fact and the next event ("No decisions
  waiting. Argus proposes new tasks after each run, next at 10:00."), an optional ghost action; no illustrations.
- **Filtered empty:** "No match for these filters" + Reset.
- **Never run:** "It has not run yet, so there is nothing to show." + the run control when one exists.
- **Working:** no spinners on reads; a job in progress shows a "Working…" pill and a 2–3 s refresh; after 30 s a warn
  banner names the job.
- **Error:** the card body becomes a fail-tinted line with the cause in plain words, the source, "Retry" and what
  still works; other cards render normally (partial failure).
- **Stale:** a warn pill "Stale · 3 h" in the card header; the top-bar freshness pill turns amber.
- **Permission:** a muted line "Admins act on this; you can read it." Never show a control a role cannot use.
- **Saved, other views catch up:** an ok banner "Saved. Other views catch up at the next sync."

### 3.21 Charts
Server data drawn as SVG inside cards: 4–5 horizontal gridlines (1px `--border`), no y-axis line, a 1px
`--text-muted` baseline, ticks 12px muted tabular, x labels flat and never rotated, value labels on marks where they
fit.
- **Bar:** square bottoms, 4px top radius, `--data-1` for the main series; a highlighted period in `--data-1` and the
  rest in `--data-1-soft`.
- **Stacked:** at most 4 segments, 2px `--surface` seams, ordered bottom → top by importance, a legend in the header
  with values. Check runs: ok `--data-green-soft`, warn `--data-sand`, fail `--data-red`.
- **Waterfall:** start and end totals `--data-grey` / `--data-slate`; increases `--data-green-soft` labelled
  `--ok-fg` "+n"; decreases `--data-red` (largest) / `--data-red-soft` labelled `--fail-fg` "−n"; 1px dashed muted
  connectors; the axis may start above zero only when the subtitle says so ("axis starts at 80%").
- **Line:** 2px `--data-1` straight segments, 6px points only on labelled samples, an end value label, dashed after
  "now" for a forecast; reference and target lines 1px dashed muted with the label at the end. No area gradient.
- **Segmented bar:** 12px high, radius 6, 2px seams (capacity: used `--data-1`, reserved `--data-1-soft`, this change
  `--data-red`, headroom `--data-grey`), a 2px `--data-target` tick for the limit; a legend below with values.
- **Categorical order:** `--data-1`, `--data-teal`, `--data-sand`, `--data-slate` (max 4). Red is reserved for bad.
- **Good or bad, not up or down:** gains green-soft, losses red, neutral totals grey or slate, the current series
  `--data-1`.
- Every chart has a "Show data" fold with the numbers as a table.
- **States:** no data in the window ("No runs in the last 48 h", the zero line drawn); partial data (a gap, not a
  zero); a single point (the value + "one sample"); error (§3.20).
- **Rule:** no chart without a data source. Error budgets and SLO burn exist only when a source provides them.

### 3.22 Disclosure
Native `<details>`/`<summary>` (or an equivalent with `aria-expanded`): 44px summary, a chevron that turns 90°
(`--t`), the open summary keeps a 1px rule under it. Folds hold method notes, raw payloads, history, long evidence.

### 3.23 Command palette and shortcuts

- **Palette:** Ctrl K (⌘K) or the top bar button opens the palette: cmdk in a Base UI Dialog (`components/Palette.tsx`). It holds actions,
  the pages of the nav and the open incidents. The library loads on first use, so pages do not carry it. Commands come
  from `/api/v1/nav`, so a role sees only its pages. With no match, Enter searches Inventory.
- **Shortcuts:** `lib/shortcuts.ts` is the one list. `g` then a letter jumps to a page; single keys run actions; `j` and
  `k` mark rows; `?` lists everything. None fire while the focus is in a field. Single-key shortcuts can be turned off
  in the palette (WCAG 2.1.4), and that choice stays in the browser.
- **Rule:** a shortcut never performs a write without the form the page already shows. Re-plan asks for confirmation, then
  presses the page's own button; from another page it only goes to Today.

### 3.24 Tailwind and shadcn components

Argus is moving to Tailwind v4 and shadcn/ui (Base UI flavour) in steps. Until the move ends, these components and the
old CSS live side by side. `app/tailwind.css` holds the setup; `test/tailwind.test.ts` guards it.

- **Containment:** no Preflight; a reset scoped to `[data-slot]` takes its place. `source(none)` plus `@source` means only
  listed folders generate CSS, so add a folder only when its old classes cannot collide with a utility (`.table`,
  `.hidden`, `.grid`). The old element reset sits in `@layer argus-reset`, under the utilities.
- **Tokens:** `@theme inline` maps the shadcn names onto `tokens.css`. Never redefine `--accent` or `--border`: both exist
  in `tokens.css` with another meaning. No colour literal in `tailwind.css`.
- **CSP:** the policy does not change. `CSPProvider disableStyleElements` sits in the root layout, with no nonce. Server
  HTML has no `style=""` and no `<style>`. Base UI parts that write `style` on the server (Checkbox, Radio, Switch, Slider,
  Meter, NumberField, Combobox, Autocomplete, open Accordion, ScrollArea, Select's hidden input, Progress, Collapsible)
  need a server-safe wrapper before use, or stay native. Bulk selection keeps native checkboxes.
- **Without JavaScript:** a page still works. `<details>` stays, `.tabs` links stay links and confirmations stay server
  steps. Dialog, Popover and Tooltip only enhance; nothing depends on them. Open Command only inside a dialog.
- **Primitives:** `components/ui` holds Button, Badge, Card, Table, Input, Textarea, Select and Label. They are server
  components with no Base UI and no client JavaScript. Form controls are native. A link styled as a button is an `<a>` with
  `data-slot="button"` and `buttonVariants()`. No focus classes: the `:focus-visible` outline in `base.css` serves them all.
- **Pages:** a page moves when its own CSS file can go: add the page to `@source`, use the primitives and utilities, delete
  the file. Anything that repeats per row goes in `@layer components` in `tailwind.css` under a name no old page uses, so
  its classes ship once and not twice per row. `Button`, `Input` and `Select` match `.btn` and `.field input`, so moved and
  old pages look the same. Shared old classes (`.fold`, `.kv`, `.phead`, `.field`) and the page CSS in `components/*.css` stay: they are in use, and rewriting them changes nothing a user sees.
  Unlayered old rules beat utilities: `text-base!` is needed on a `.fold` summary, and a link with `data-slot` has no hover
  underline. Today and the Review queue and panel have moved; the Review full lists still use `queue.css`.
  Incidents has no CSS of its own: it sits on `Section.tsx` and `section.css`, shared by about 60 files. Its cards, buttons and
  fields use the primitives, and `TitledCard` is the common titled card; `section.css` goes when its last user moves.
  `PlainCard` is `.card` as a component: padding on all sides and no `[data-slot]` reset, for pages still built on `.card-h`
  and `.card-sub`. Every page now uses Button and PlainCard or Card; `.btn`, `.card`, `.pill` and `.banner` are gone from `base.css`; `.pill` and `.banner` are `@layer components` rules now. Button and Card keep their base styles in `.ui-btn` and `.ui-card`, so each ships one short class.
- **Dialogs:** `components/ui/dialog.tsx` is Base UI Dialog, and `command.tsx` is cmdk, used only inside a dialog. The palette
  and the shortcut list load on first use, so no page pays for them before that. Popover and Tooltip are not added: nothing
  needs them yet.
- **Shell:** the server-rendered `Sidebar.tsx` stays and is restyled. The shadcn Sidebar is not used: it adds about 64 KB
  of gzipped JavaScript to every page and leaves phones with no navigation before JavaScript runs.

## 4. Page templates

### Overview / landing page
Page header → Needs-you card (ranked) → the page's answer (stat tiles or one chart) → supporting cards in the
12-column grid → folded detail. Every group's first page leads with "Needs you".

### Queue page
Page header → kind tabs with counts → filter pill bar (search first) with the result meta → the queue (rows or a
table) with a contextual bulk bar → pagination "1–25 of 61". Sort defaults to a documented priority (severity,
environment, deadline, waiting time) and says so in the status line. Every filter, sort and page is a URL parameter;
every change returns to page 1.

### Detail page
Identity and status in the header (title, status pill, env pill, the immediate actions), a stepper where the object
has stages, the facts as a two-column definition list (`dl`), evidence as activity rows, then folded history and raw
details. An "On this page" jump row when the page is long.

### Settings and forms
Settings sections as tabs under the header. Grouped fields with a label, a hint and an inline error; a sticky save bar
with one primary; a rejected save re-renders the form with the values kept and the error on the field. An action that
cannot be taken back is confirmed where it sits, with its consequence in words.

## 5. Screens

### Today (`/today`)
- **Question:** "What needs you now, and in what order?"
- **Header:** h1 "Today"; the question; status line "Planned 6 · ~2 h 10 m of your 3 h · 1 done · Tue 3 Nov"; right:
  secondary "Re-plan" (admins). Under it, a segmented bar the width of the content: done (`--data-1`), planned
  remaining (`--data-1-soft`), decisions waiting (`--data-sand`), free time (`--data-grey`), a target tick at the
  available hours; a legend with minutes.
- **Row 1 (12 cols):** the Needs-you card in today's plan order, at most 4 findings (e.g. a production backup set
  failing three nights, a certificate expiring, an incident waiting for triage, a CI/CD failure on release). The
  first finding also carries the page's one primary, "Start".
- **Row 2:** "Plan for today" (cols 1–8): ordered activity rows (a done-check circle as a 40px submit target, title,
  "Do this" meta, estimate right, "Open →"); Later and Done today fold in the card footer. "Review inbox" (cols
  9–12): "Decisions waiting for you · most urgent first", the figure "7 waiting · ~25 min", the top 4 queue rows,
  "Open Review inbox →".
- **Row 3:** "Checks, last 48 h" (cols 1–8): stacked hourly bars of check runs, "Scheduled check runs per hour · all
  environments", the worst hour annotated. "Open incidents" (cols 9–12): the top incident with a compact vertical
  stepper and its time-in-stage pill, the others as rows.
- **States:** all clear (the Needs-you ok sentence, the plan card leads); planner off (findings from health exceptions,
  the plan card replaced by health stat tiles); stale data; viewer (no Start, "An admin's next step"); a job pending
  after an action ("Saving…" pill, refresh, then the server banner). Phone: one column in the same order.

### Review inbox (`/review`)
- **Question:** "Which decisions are waiting for you, most urgent first?" Status line: "Sorted by urgency: severity,
  environment, deadline, waiting time". Right: ghost "Open full list".
- **Kind tabs** with counts: All · Task proposals · Action approvals · Lessons · Ideas · Chat requests · Problem links.
- **Filter pill bar:** search (280px), Severity, Environment, Source, Sort, Reset when active; meta "61 waiting · ~2 h".
- **Body ≥ 1200px:** the queue card (cols 1–7) and the panel card (cols 8–12, sticky under the top bar). Queue rows
  as §3.11; footer "1–25 of 61" Prev / Next.
- **Panel:** header pills (kind tag, severity, env), title 22/30, "What you are deciding" in one sentence; for action
  approvals a stepper; facts in two columns (affected targets mono, decision deadline, operational deadline "None
  recorded", waiting since with the absolute time); "Proposed outcome" at 68ch; "Evidence" as activity rows or a warn
  box "No evidence recorded" with the source link; an info callout "If you accept" (what the system will do); a sticky
  action bar: primary with an explicit verb ("Create Jira issue"), secondary "Edit", ghost "Snooze 24 h" (only when the
  expiry is more than 24 h away, with "expiry keeps running"), danger-ghost "Reject…" as a fold with the reason field
  and its own submit; History and raw details folded.
- **Narrow (< 1200px):** the panel replaces the queue, with "Back to queue" restoring the row's anchor.
- **States:** empty; filtered empty with Reset; one kind failed to load (a fail row naming the kind, the others
  shown); decision done (back to the queue with the server's banner); viewer ("Admins decide this; you can read it");
  the plan step (the plan diff and "Approve plan v2 & start").

## 6. Accessibility

WCAG 2.2 AA. Text ≥ 4.5:1 and non-text controls ≥ 3:1 (checked by `scripts/contrast.mjs`). Focus: a 2px `--focus`
ring offset 2px on every interactive element (inset in the sidebar), never removed. Semantic landmarks, one h1, real
labels, `aria-current` for the current page, step, tab and selected row. Status in words and glyphs, never colour
alone. Targets ≥ 44px on coarse pointers. Keyboard: every action reachable in DOM order; Esc closes overlays and
returns focus. Charts have a data table. Relative times carry the absolute time (`<time datetime>` + `title`). Long
technical strings wrap or truncate with the full value available. Test at 360, 768, 1024, 1280 and 1440+ px, 200%
zoom, both themes.

## 7. Do and don't

| do | don't |
|---|---|
| one primary per screen or panel, an explicit verb | the accent on every button, tab or chrome background |
| a sentence title + a subtitle with the window | an eyebrow label above a heading, uppercase tracked labels (outside the sidebar) |
| one card split into stat tiles, figures as type | a tile per KPI with an icon and a sparkline |
| status as tint + word + glyph | red for production, colour as the only signal |
| rules and alignment in tables | zebra rows, header bands, left-aligned numbers |
| a border or a tone step for surfaces | shadows at rest, glows, glass, gradients |
| the real number at its real size, "—" for missing | invented totals, placeholder people, fake error budgets |
| empty states that state the fact and the next event | illustrations, "No data yet" with nothing else |
| motion on press, hover, disclosure, overlays | entrances, counters ticking, charts drawing in |

### Board columns

All columns share the canvas (`flex: 1 1 0`, at least 236px): six fit from 1800px. Below that the board scrolls sideways
and says so: a row of column chips with counts jumps to each column, and the scrollbar stays visible. Phones snap one
column at a time. A card stays short: a plan with write steps shows a WRITE pill and a step count, and folds the summary
and the steps; a plan without them shows its summary, clamped to 3 lines.

## 8. Deliberate deviations (accepted by the operator, 2026-11-03)

| deviation | reason |
|---|---|
| Accent moves from teal to violet `#5b4bb7`, including the primary button fill (was near-black ink) | operator's choice of direction; mitigated by scarcity (the accent list in §2.1, ≤ ~3% of a screen) and no gradients |
| The framed-sheet shell becomes an open plane: sidebar and canvas share `--bg`, white cards carry the work | cards with sentence titles are the unit of the new direction; a sheet around cards would nest surfaces |
| Larger radii: cards 14, controls 8, filter pills 10 (were sheet 16 / card 8 / control 6) | the calmer, softer card language of the direction |
| Small caps for the sidebar group labels only | quiet section labels that do not compete with the links; scoped to the sidebar, sentence case everywhere else |
| 32px icon circles on activity rows and findings | neutral kind markers, or status tints that carry meaning; never decoration on KPIs |
| A 56px top bar (crumb, search, freshness) | freshness of the data is decision-critical; search needs a fixed place |
| Dark `--accent-hover` is `#5d4bbd`, not the direction's `#7a68d6` | white on `#7a68d6` is 4.39:1, below AA; `#5d4bbd` is 6.55:1 |

## 9. Hard constraints

- Strict CSP: `default-src 'none'; style-src 'self'; img-src 'self'; font-src 'self'`; no inline `style=""`, no
  `<style>`, no external font, CDN or image. A frontend that needs script loads it from its own origin only.
- Fonts and icons are self-hosted; nothing is loaded from a third party.
- Every value from the database, agent output, logs or chat is escaped before it is rendered; Markdown is escaped
  first, then formatted.
- Security is not design: routes, authentication, CSRF tokens, form field names, the request-file flow and
  validation never change as a side effect of a restyle.
- Colour literals live only in `tokens.css`; the contrast script passes before a token change merges.
- Stylesheets stay small and layered (tokens → base → components → page families); each layer has a byte budget
  enforced by a test in the frontend that ships it.
- The current server-rendered pages still use `static/*.css` with their own budgets (enforced by the app's CSS budget
  test) until the new frontend replaces them; those sheets are not the source of truth for new work.

## 10. Known mistakes: never repeat

Every row is a fault the operator found on a live page and that was fixed. A new page, a moved page and a restyle are
checked against this list before they merge. `demo/layout-sweep.mjs` catches the measurable ones (rows marked *sweep*).

| mistake | rule |
|---|---|
| Old unlayered CSS silently beats a Tailwind utility (`.ncount` stayed visible under `rail:hidden`) | check the computed style, not the class list; override an old rule with an unlayered rule next to it, or move the old rule into a layer |
| A form row with fixed columns in a half-width card hid its labels under the selects (Settings → General) | size by the container, not the window: `.set-rows` is a container, rows stack below 560px of card width. *sweep* |
| Count pills on the 72px icon rail spilled past it, and Linux Chrome drew a scrollbar inside it | on the rail a count is a dot on its icon (red for alarms); the rail scrolls without a scrollbar. *sweep* (rail mode) |
| Row actions as nested `<details>` folds inside a table cell (Users) | one Actions menu per row; each risky action opens its own confirm panel with the consequence in words; one panel at a time |
| A lonely wide card on an empty page (2FA setup) | a single-task page is a centred column (about 640px) with status, why, steps and one primary |
| Secondary text squeezed beside a pill in a narrow column (2FA "used …" at 1024px) | put it on its own line; dates and times do not break (`.nw`). *sweep* |
| Tall stat tiles pushed a list page's first row below half the screen (Incidents) | on a page whose table is the answer use `<Tiles compact>`; keep the subtitle to one line |
| Filter selects as wide as their longest option, and a filter bar on two lines | `field-sizing: content` on filter selects; icon-only Reset, no visible result count: one line at 1440px. *sweep* (Incidents) |
| Board cards of very different heights | titles and plan text clamp to 2 lines; a write plan folds behind its WRITE line |
| A class codemod matched `btn` inside `check-btn` and turned ticks into filled buttons | match class names on word boundaries; compare every page that uses the old class before merging |
| `shrink-0` on Button overflowed at 390px | no fixed-width or non-shrinking utilities on shared primitives; check 390px. *sweep* |
| A catch-all route swallowed fallback rewrites | catch-all and not-found routes run after rewrites (`afterFiles`) |
| A click reloads the page or jumps to the top (sidebar Collapse, filter Apply, a tab, the theme, a sidebar link) | with JavaScript a click is never a document load: GET forms are `GetForm` (`next/form`), tabs, chips and pager links `SoftLink`, every other plain `<a>` to an app page goes through `Navigate` (`Soft.tsx`), UI preferences flip in the browser (`Instant.tsx`), and an action that comes back to its page ends with `return back(here)` (`lib/server.ts`), which re-renders in place; the plain form or link stays the no-JS fallback, so every one still works without JavaScript |
| After a client navigation the sidebar and top bar still showed the old page (current link, open group, crumb, counts, freshness) | a layout is not re-rendered by a client navigation: anything in the shell that depends on the page is a parallel route slot (`app/(app)/@side`, `@top`, `components/Shell.tsx`), never computed in `layout.tsx`; shared reads go through the `cache()`d `shell*` helpers in `lib/server.ts` |
| A page that refreshes itself pulled the user back to it seconds after they had gone to another page (Board task) | a bare `<meta http-equiv="refresh">` survives a client navigation: the browser already scheduled it. Use the shared `Reload` (`components/Reload.tsx`; `Refresh` in `Section.tsx` wraps it): a timer that re-renders in place, cleared when the page goes, paused while the user writes; the meta refresh only inside `<noscript>` |
| "Sent … this page refreshes itself" waited forever when the job that applies requests did not run | a request page stops waiting: after 60 s unread (or at once when the job's last run is old) it shows a `fail` banner that says nothing changed and what to check (`requestWaitBanner`) |
| React #418 hydration errors on slow networks | keep `lib/hydrate-after-parse.ts`; recheck it after any Next upgrade |
| A native file input: a text box with the browser's grey "Choose File" button inside (Kubernetes upload) | file fields use the shared `input[type="file"]` style in `base.css`: a dashed drop zone with an outline button; never restyle one per page |
| A flash message inside the first column of a two-column page pushed that column down, so the columns no longer started level (Settings → Claude) | messages (`.banner`) span every column (`column-span: all`) and sit above the cards; never inside one column |
| Copy with internal or company names | product words only (Argus, "chat", "cache edge"); the leak check enforces it |
| Status shown by colour alone, or a new icon set per page | severity is the signal-bar glyph from `Severity.tsx`; status is tint + word + glyph (§7) |

### New page checklist

1. Pick the template in §4 and copy the closest existing page; use `Section.tsx` (`Tiles`, `Filters`, `Banners`,
   `Pager`), `ui/card.tsx` (`PlainCard`, `TitledCard`), `ui/button.tsx` and `Field`. Do not add a new card, tile,
   filter, pill or button style.
2. Read §10 above and check each row that applies.
3. Strict CSP (§9): no `style=""`, no inline script; the page works without JavaScript.
4. Run `node demo/layout-sweep.mjs <url>` against a running build: 0 findings at 1440, 1280, 1024 and 390px, with the
   sidebar open and collapsed.
5. Add the page to `demo/smoke.py` and give it demo data, so the demo never shows it empty.

