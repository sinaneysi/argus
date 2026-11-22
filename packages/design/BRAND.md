# Argus brand book

Goal: show how Argus looks, sounds and presents itself, in one page.

This file is the brand layer. It does not repeat the UI contract. For components, states and page rules, read
[DESIGN.md](DESIGN.md) ("Harbor") and [DESIGN_SYSTEM.md](DESIGN_SYSTEM.md). Where this file and DESIGN.md disagree,
DESIGN.md wins. Every value here comes from [tokens.css](tokens.css). When a value changes there, change it here.

| fact | value |
|---|---|
| Product | Argus, an open-source DevOps operations console |
| Licence | see [LICENSE](../../LICENSE) |
| Mark | [`brand/mark.svg`](brand/mark.svg) (light), [`brand/mark-dark.svg`](brand/mark-dark.svg) (dark), [`brand/mark-512.png`](brand/mark-512.png) |
| Favicons | [`static/favicon.svg`](static/favicon.svg), `favicon-32.png`, `favicon.ico` (16/32/48), `apple-touch-icon.png` (180) |
| Tokens | [`tokens.css`](tokens.css) |
| Contrast gate | `node packages/design/scripts/contrast.mjs` |
| Fonts | [`static/fonts/`](static/fonts/): IBM Plex Sans, JetBrains Mono, Vazirmatn (SIL OFL 1.1) |
| Writing style | [`apps/api/internal/style/default.md`](../../apps/api/internal/style/default.md) |

## 1. What Argus is

Argus collects evidence from alerts, logs, metrics, CI pipelines, backups, certificates and host checks. It asks a
language model to explain the evidence. It gives people one page for each problem, with the exact fix.

- Argus is read-only by default.
- A change to a system goes through a human approval first.
- Argus is a work tool. Every screen is an app screen, not a landing page.

**Name.** Argus is the watchman with a hundred eyes. The product watches so that people can sleep.

**Mood:** calm, friendly, roomy, legible, assured.
**Not:** a wall of red tiles, a neon "hacker" theme, KPI tiles with icons, decoration.

### 1.1 Name usage

| do | don't |
|---|---|
| "Argus" with a capital A, in running text | "ARGUS", "argus" in prose, "Argus AI", "the Argus" |
| "Argus proposes a fix." (the product is the subject) | "Our AI magically fixes it." |
| The install name beside the mark, in muted text ("Argus · Example Corp") | A company logo inside the mark tile |

Lowercase `argus` is correct only in code, package names, URLs and commands.

## 2. The mark

An eye. The upper lid peaks like the letter A. The lower lid is a soft curve. The iris is a filled circle. The eye
sits on a square tile with round corners.

| part | value |
|---|---|
| Tile | 32 × 32 units, corner radius 8 |
| Lids | stroke 2.6, round caps and joins: `M6 18 16 8l10 10` (upper), `M6 18q10 9 20 0` (lower) |
| Iris | circle at (16, 17.5), radius 3.2 |
| Light version | tile `#1459c7` (`--mark-bg`), lines `#ffffff` (`--mark-fg`), 6.40:1 |
| Dark version | tile `#7fb2ff` (`--mark-bg`), lines `#06182f` (`--mark-fg`), 8.24:1 |

### 2.1 Versions

| file | use |
|---|---|
| `brand/mark.svg` | light theme, documents, the favicon set |
| `brand/mark-dark.svg` | dark theme, dark slides, the dark ground `#0b1220` |
| `brand/mark-512.png` | places that cannot show SVG (app stores, chat avatars, social cards) |
| `static/favicon.*`, `apple-touch-icon.png` | browser tabs and home screens; always the light tile |

Use the version that matches the ground. On a light ground use `mark.svg`. On a dark ground use `mark-dark.svg`.

### 2.2 Lockup

In the sidebar the mark is 28px. "Argus" is beside it in IBM Plex Sans 18/600, in `--text`. The install name is
under or beside it in 12px `--text-muted`. Keep this order: mark, name, install name.

### 2.3 Clear space and minimum size

| rule | value |
|---|---|
| Clear space | at least 1/4 of the tile width on each side (8px for a 32px tile). Keep text, edges and other marks out. |
| Minimum size, screen | 16px (the smallest favicon) |
| Minimum size, lockup | 24px tile with the name |
| Minimum size, print | 6mm |

### 2.4 Misuse

Do not do these things:

- Do not stretch, skew or rotate the mark.
- Do not recolour the mark for status. A failure never makes the mark red.
- Do not add an outline, a shadow, a glow or a gradient.
- Do not remove the tile and show the eye lines alone on a busy ground.
- Do not put the mark on a photo or a pattern.
- Do not redraw the mark. Copy the SVG from `brand/`.
- Do not use the light tile on the dark ground. Use `mark-dark.svg`.

### 2.5 History

The first version of this mark (November 2026) used a teal tile, `#1b6c80`. The tile changed to the ocean-blue accent
when the "Harbor" colourway was chosen. The geometry did not change. Do not use the teal version.

## 3. Colour

Colour carries meaning, never decoration. Each colour is one `light-dark()` pair in `tokens.css`. A colour literal
outside `tokens.css` is a bug.

### 3.1 Roles

| role | tokens | light | dark | use |
|---|---|---|---|---|
| Ground | `--bg` | `#dce8f6` | `#0b1220` | canvas, sidebar, top bar |
| Surface | `--surface` | `#ffffff` | `#131c2e` | cards, panels, tables, inputs |
| Well | `--surface-2` | `#ebf2fa` | `#1d2840` | code, neutral pills, tab track |
| Hover | `--hover` | `#e6eef8` | `#1d2840` | row, nav and menu hover |
| Border | `--border` | `#dfe8f3` | `#2a3650` | card edges, row rules (decorative, below 3:1 on purpose) |
| Control border | `--control-border` | `#66788f` | `#6e8099` | input, checkbox and radio edges |
| Ink | `--text` | `#0f1b2d` | `#e8eff8` | titles, figures, identity |
| Ink 2 | `--text-2` | `#2f3f55` | `#c2cedd` | running text |
| Muted ink | `--text-muted` | `#4f6178` | `#95a6bc` | labels, meta, subtitles |
| Accent | `--accent`, `--accent-strong` | `#1459c7` | `#7fb2ff` | the one primary button, links, selection, focus, the mark |
| Accent hover | `--accent-hover` | `#0f49a8` | `#a1c7ff` | primary button hover |
| On accent | `--accent-contrast` | `#ffffff` | `#06182f` | text on the accent fill |
| Selected | `--accent-soft` / `--accent-soft-fg` | `#dce9fc` / `#0f4aa6` | `#16304f` / `#a9cbff` | selected nav item, row, filter |
| Focus | `--focus` | `#1459c7` | `#7fb2ff` | the focus ring |
| OK | `--ok-bg` / `--ok-fg` | `#e2f3e7` / `#1f6b3a` | `#14291c` / `#7ad79d` | healthy, done |
| Warn | `--warn-bg` / `--warn-fg` | `#fcf0d9` / `#835000` | `#2d2410` / `#ecbb60` | degrading, expiring, stale |
| Fail | `--fail-bg` / `--fail-fg` | `#fdeaea` / `#b42f33` | `#331719` / `#ff9191` | broken, at risk, destructive |
| Info | `--info-bg` / `--info-fg` | `#e6eff8` / `#2f5a86` | `#17263a` / `#9cc1ea` | neutral notice |

Chart colours (`--data-*`) are in [DESIGN.md §2.1](DESIGN.md) and §3.21. They say "good or bad", not "up or down".

### 3.2 Rules

1. Use the accent only for action and selection. Keep it to about 3% of a screen.
2. Use status colours only for state. Always add a word, and on rows a glyph.
3. Name production, do not alarm it: an ink `prod` pill with an ink dot, never red.
4. Do not use gradients, glows or glass.
5. Put text on a fill only with its pair token (`--accent-contrast` on `--accent-strong`, `--ok-fg` on `--ok-bg`).

### 3.3 Contrast

`scripts/contrast.mjs` checks every documented pair in both themes (WCAG 2.2 AA). Result on this version: **all pairs
pass**. The lowest pair in each group:

| pair | minimum | light | dark |
|---|---|---|---|
| `--text` on any surface | 4.5 | 13.92 (on `--bg`) | 12.68 (on `--surface-2`) |
| `--text-2` on any surface | 4.5 | 8.62 (on `--bg`) | 9.21 (on `--surface-2`) |
| `--text-muted` on any surface | 4.5 | 5.11 (on `--bg`) | 5.92 (on `--surface-2`) |
| `--accent` as text | 4.5 | 5.15 (on `--bg`) | 6.79 (on `--surface-2`) |
| `--accent-contrast` on `--accent-strong` | 4.5 | 6.40 | 8.24 |
| `--accent-soft-fg` on `--accent-soft` | 4.5 | 6.71 | 8.07 |
| `--mark-fg` on `--mark-bg` | 4.5 | 6.40 | 8.24 |
| status fg on its own bg | 4.5 | 5.34 (fail) | 7.60 (fail) |
| `--focus` on `--bg` / `--surface` | 3 | 5.15 | 7.88 |
| `--control-border` on `--bg` / `--surface` | 3 | 3.64 | 4.23 |
| chart marks on `--surface` | 3 | 3.09 (`--data-red`) | 3.60 (`--data-slate`) |

Run the script again after every token change. A change that fails does not merge.

## 4. Typography

| family | file | weights | use |
|---|---|---|---|
| IBM Plex Sans | `ibm-plex-sans-var-latin.woff2` | 100–700 (variable) | all UI text |
| Vazirmatn | `vazirmatn-arabic.woff2` | 100–900 (variable) | Arabic script only, `size-adjust: 110%` |
| JetBrains Mono | `jetbrains-mono-var-latin.woff2` | 100–800 (variable) | hosts, IDs, SHAs, paths, logs, code |

The fonts are self-hosted. Do not load a font from a third party.

| token | size / line | weight | use |
|---|---|---|---|
| `--fs-2xl` | 32 / 40 | 600, −0.02em | page title |
| `--fs-fig` | 32 / 40 | 600, tabular | the headline figure |
| `--fs-xl` | 22 / 30 | 600 | panel title |
| `--fs-lg` | 18 / 26 | 600 | card title |
| `--fs-md` | 16 / 24 | 400 | body, buttons, subtitles |
| `--fs-sm` | 14 / 20 | 400 / 500 | table cells, row meta |
| `--fs-xs` | 13 / 18 | 400 / 500 | meta, pills, headers; the smallest size a person reads |
| `--fs-code` | 13 | 400 | JetBrains Mono |

- Use sentence case everywhere. Do not use all caps, except the sidebar group labels.
- Use tabular figures on every number. Use a true minus (−) on signed changes.
- Keep running text to `--measure` (62ch).
- Do not use mono for labels, pills or headings.

## 5. Space and shape

| group | tokens |
|---|---|
| Space (4px base) | `--s1` 4, `--s2` 8, `--s3` 12, `--s4` 16, `--s5` 24, `--s6` 32, `--s7` 48 |
| Layout | `--pad` 32 (24 at ≤ 1280px, 16 at ≤ 900px), `--gap` 16, `--gap-block` 20, `--card-pad` 22 |
| Controls | `--control` 40, `--control-sm` 34, `--control-lg` 44, `--target-coarse` 44 |
| Radii | `--r-chip` 6, `--r-control` 12, `--r-inner` 14, `--r-filter` 14, `--r-overlay` 16, `--r-card` 20, `--r-pill` 999 |

- Buttons are pills (`--r-pill`). Cards have `--r-card`. Inputs have `--r-control`.
- Separate surfaces with a border or a tone step. Use a shadow only on what floats (menus, dialogs).
- Inner radius = outer radius − padding, at least 4.

## 6. Iconography

- Use Lucide (ISC licence), outline, 1.5px stroke.
- Use 16px icons in nav, buttons and pills. Use 18px icons inside 32px circles.
- Inline the icons. Set `aria-hidden="true"` and `currentColor`.
- Severity is the signal-bar glyph (`apps/web/components/Severity.tsx`): 4 bars for critical, 1 bar for low.
- Status glyphs: fail = filled circle-x, warn = triangle, ok = check, info = i.
- Do not use sparkles or other "AI" glyphs. "Needs you" uses the Argus eye.
- Do not use emoji in the UI.

## 7. Voice and tone

Argus writes like a careful senior engineer on call: short, exact, calm. The rules come from the default writing style
(Argus Settings → Writing style). Write about 80% to ASD-STE100 (Simplified Technical English).

### 7.1 Rules

1. Write one idea in each sentence.
2. Keep a step to 20 words or fewer. Keep a description sentence to 25 words or fewer.
3. Use the active voice. Use the imperative for steps.
4. Use one word for one meaning.
5. Do not make noun clusters of more than 3 words.
6. Keep every value, command, path and error string exactly as it is.
7. Never write a secret. Write the name of the variable or file that holds it.
8. Say what changed and what did not. Say what happens next.

### 7.2 Layout of longer text

Start with a one-line goal. Give a facts table. Use numbered sections and one action per checkbox. Give the reason as
a sub-bullet that starts with "Reason:". End with a "Warnings" list.

### 7.3 Do and don't (real Argus copy)

| do (from the UI) | don't |
|---|---|
| "Create Jira issue" | "Accept" (the outcome is not clear) |
| "Nothing needs you. Next checks at 10:00." | "No data yet" |
| "Every live certificate is OK." | "Woohoo! All good! 🎉" |
| "Argus could not read this page's data. Nothing was changed." | "Oops, something went wrong." |
| "Admins decide this; you can read it." | "You don't have permission to perform this action at this time." |
| "Agents only propose: the task waits in To do until a person moves it." | "Our autonomous agents handle everything for you." |
| "Asks Claude one short question. Changes nothing." | "Leverage AI-powered insights." |
| "Each recovery code works once. No codes left? Ask another admin to reset your two-factor authentication." | "Recovery codes are single-use credentials which, once consumed, cannot be reused." |
| "Backup set db-main failed 3 nights in a row" (DESIGN.md example: the number is in the title) | "Backup issues detected" |

### 7.4 Tone by moment

| moment | tone | example |
|---|---|---|
| Healthy | quiet, one fact | "Every applicable check passed." |
| Needs action | direct, the number first | "Backup set db-main failed 3 nights in a row" |
| Error | the fact, what did not change, what to check | "Argus could not read this page's data. Nothing was changed." |
| Production | named, never alarmed | an ink `prod` pill |
| Pending | the verb in progress | "Creating…" |

## 8. UI principles (summary)

The full contract is [DESIGN.md](DESIGN.md). Read §10 (known mistakes) before every UI change.

1. The most urgent thing leads. Every landing page opens on "Needs you".
2. Every card says what it shows: a sentence title and a subtitle with the window.
3. Colour carries meaning. A word and a glyph go with every status colour.
4. Figures are type. No icons on KPIs, no sparklines.
5. Tables are rules and alignment. No zebra rows.
6. Only real data. Missing is "—", zero is "0".
7. The server is the truth. Filters live in the URL. Success shows only after the server confirms it.
8. Motion is quiet: press, hover, disclosure and overlays only.
9. Strict CSP, no inline styles, and every page works without JavaScript.

## 9. Product imagery

Rules for demos, screenshots, slides and marketing material:

1. Use the public demo or the demo data generator. Use synthetic data only.
2. Never show real customer data, real hostnames, IP addresses, people, tokens or company names.
3. Use fictional names and example domains (`example.com`, `db-main`, `Alex Doe`).
4. Show the real UI at its real size. Do not mock up features that do not exist.
5. Do not invent numbers, error budgets or SLOs that the product does not show.
6. Show both themes when the theme matters. Use the matching mark version.
7. Crop to a whole card or a whole page. Do not crop away the card title and subtitle.
8. Do not add device frames with glare, gradients, or stock photos of people.

## 10. Warnings

- **Source of truth:** values live in `tokens.css`. This file follows it.
- **Contrast:** run `scripts/contrast.mjs` before a token change merges.
- **Mark:** copy it from `brand/`. Never redraw it.
- **Public repository:** no company names, hosts, IP addresses or people in any text or image.
