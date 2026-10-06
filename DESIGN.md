---
name: SwarmCracker
description: Daylight container-terminal landing page for a Firecracker microVM executor — concrete, steel, and safety orange.
colors:
  concrete: "#E8E6E0"
  concrete-deep: "#DCD9D1"
  paper: "#FBFAF7"
  asphalt: "#1B1D20"
  asphalt-soft: "#2A2D31"
  steel: "#868C93"
  steel-dark: "#4C5259"
  rule: "#C3C0B8"
  ink-muted: "#565C63"
  safety: "#E4570F"
  safety-deep: "#C24A0C"
  safety-wash: "rgba(228, 87, 15, 0.09)"
  rust: "#A33D08"
  hazard: "#F5C518"
  on-safety: "#1B1D20"
  gate-bg: "#24272B"
  gate-bg-hover: "#2E3237"
  gate-border: "#3A3F45"
  gate-arrow: "#8A9096"
  dark-muted: "#B6BAB4"
  dark-body: "#C4C7C2"
  log-note: "#9AA09A"
  log-code: "#C9CCC7"
typography:
  display:
    fontFamily: "'Big Shoulders Display', 'Archivo Narrow', 'Arial Narrow', sans-serif"
    fontSize: "clamp(2.75rem, 6.6vw, 5.25rem)"
    fontWeight: 800
    lineHeight: 0.92
    letterSpacing: "-0.02em"
  headline:
    fontFamily: "'Big Shoulders Display', 'Archivo Narrow', 'Arial Narrow', sans-serif"
    fontSize: "clamp(2.25rem, 5vw, 3.75rem)"
    fontWeight: 800
    lineHeight: 0.95
    letterSpacing: "-0.01em"
  title:
    fontFamily: "'Big Shoulders Display', 'Archivo Narrow', 'Arial Narrow', sans-serif"
    fontSize: "1.625rem"
    fontWeight: 700
    lineHeight: 1
    letterSpacing: "0.02em"
  body:
    fontFamily: "'Archivo', system-ui, -apple-system, sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.6
  label:
    fontFamily: "'Big Shoulders Display', 'Archivo Narrow', 'Arial Narrow', sans-serif"
    fontSize: "1.0625rem"
    fontWeight: 700
    lineHeight: 1
    letterSpacing: "0.06em"
  mono:
    fontFamily: "'B612 Mono', ui-monospace, 'SFMono-Regular', monospace"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.6
    letterSpacing: "0.02em"
rounded:
  none: "0px"
  circle: "50%"
components:
  button-primary:
    backgroundColor: "{colors.safety}"
    textColor: "{colors.on-safety}"
    typography: "{typography.label}"
    rounded: "{rounded.none}"
    padding: "0.85rem 1.5rem"
  button-primary-hover:
    backgroundColor: "{colors.hazard}"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.asphalt}"
    typography: "{typography.label}"
    rounded: "{rounded.none}"
    padding: "0.85rem 1.5rem"
  button-ghost-hover:
    backgroundColor: "{colors.asphalt}"
    textColor: "{colors.paper}"
  nav-cta:
    backgroundColor: "{colors.safety}"
    textColor: "{colors.on-safety}"
    typography: "{typography.label}"
    rounded: "{rounded.none}"
    padding: "0.6rem 1.25rem"
  spec-plate:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.asphalt}"
    rounded: "{rounded.none}"
  spec-plate-rail:
    backgroundColor: "{colors.asphalt}"
    textColor: "{colors.paper}"
    typography: "{typography.mono}"
    padding: "0.45rem 0.7rem"
  container-box:
    backgroundColor: "{colors.steel}"
    textColor: "{colors.asphalt}"
    typography: "{typography.mono}"
    rounded: "{rounded.none}"
    padding: "0.85rem 1rem"
  container-box-lead:
    backgroundColor: "{colors.safety}"
  gate-card:
    backgroundColor: "{colors.gate-bg}"
    textColor: "{colors.paper}"
    rounded: "{rounded.none}"
    padding: "1.5rem 1.35rem 1.35rem"
  gate-card-hover:
    backgroundColor: "{colors.gate-bg-hover}"
  terminal-log:
    backgroundColor: "{colors.asphalt}"
    textColor: "{colors.paper}"
    rounded: "{rounded.none}"
    padding: "1rem 1.1rem 1.2rem"
  terminal-log-head:
    backgroundColor: "{colors.hazard}"
    textColor: "{colors.asphalt}"
    typography: "{typography.label}"
    padding: "0.5rem 0.9rem"
  manifest-row:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.asphalt}"
    rounded: "{rounded.none}"
  manifest-seq:
    backgroundColor: "{colors.concrete-deep}"
    textColor: "{colors.ink-muted}"
    typography: "{typography.mono}"
  comparison-highlight-col:
    backgroundColor: "{colors.safety-wash}"
    textColor: "{colors.asphalt}"
---

# Design System: SwarmCracker

## Overview

**Creative North Star: "The Working Yard"**

SwarmCracker's landing page is an intermodal freight terminal in flat daylight. The page is not decorated to look like a terminal; it is built out of the terminal's own parts. The ground is concrete, the rules are steel, the plates are stencilled, and every workload is drawn as a sealed corrugated container with a stencilled ID and a lock state. The metaphor is the argument: SwarmKit is a gantry crane that places each service replica as its own hardware-isolated box, and the visitor watches the mechanism work rather than being told it works.

The world is deliberately light. Where the category defaults to a dark near-black hero with a neon terminal glow, this system forces a concrete `#E8E6E0` ground and reserves dark asphalt `#1B1D20` for bounded plates and whole bounded sections — the docs gateway, the footer, the placement log, the lead seal. Safety orange `#E4570F` is not an accent sprinkled on top; it owns entire regions (the crane spreader, a container door, the primary CTA, the lead container, the active comparison column), which is what makes the palette read as plant equipment instead of a brand color.

Everything is square and hard-edged. There is no border radius except the circular container seal, and there are no cast shadows; depth is carried by tonal layering between concrete, paper, and asphalt, and by structural borders of 1px, 2px, 4px, and 6px. Type is a three-voice system — condensed industrial display for signage, Archivo for reading, B612 Mono for anything that is an identifier or a measurement. The result is dense, legible, and unmistakably a working yard rather than a landing-page template.

**Key Characteristics:**
- Daylight concrete ground; dark asphalt only as bounded plates and sections.
- Safety orange applied as owned regions, never as a hairline accent.
- Square corners everywhere; a single circle for the container seal.
- No cast shadows; depth from tone and structural borders.
- Condensed display caps + grotesque body + tabular mono for IDs.
- One authored motion: the crane traverses, lowers, and places a container.
- Deliberately uneven 12-column spec board instead of a uniform card grid.

## Colors

A concrete-and-steel neutral field cut by two industrial signal colors — safety orange that owns whole regions and hazard yellow that marks edges and state.

### Primary
- **Safety Orange** (`#E4570F`, `--yard-safety`): The load-bearing color. Fills the primary CTA, the lead container box, the crane spreader, the nav CTA, the comparison highlight column, the skip link, and the mobile-menu underline. It is always a filled region, never a hairline.
- **Deep Safety** (`#C24A0C`, `--yard-safety-deep`): The focus-ring orange and the caret color. Used where orange must sit on light concrete without being a large fill.
- **Rust Orange** (`#A33D08`, `--yard-rust`): The only orange safe for body text on concrete (`5.2:1`). Carries the brand-word hover and the security record icons.

### Secondary
- **Hazard Yellow** (`#F5C518`, `--yard-hazard`): Marking and state color. Fills the hazard kerbs, the placement-log header, the spec-plate reference text, the gate-card top border, and dark-surface hover text. It is the yellow of painted floor tape, not a highlight.

### Tertiary
- **Concrete** (`#E8E6E0`, `--yard-concrete`) and **Concrete Deep** (`#DCD9D1`, `--yard-concrete-deep`): The page ground and its recessed variant (scrollbar track, manifest sequence gutter, comparison header cells).
- **Paper** (`#FBFAF7`, `--yard-paper`): The raised plate surface — spec plates, manifest body, seal records, diagram frame. Reads as a bright slab on the concrete.

### Neutral
- **Asphalt** (`#1B1D20`, `--yard-asphalt`, aliased as `--yard-ink`): The ink and the dark-section ground. Body text, all structural borders, the docs gateway, the footer, the log, the lead seal, and every rail.
- **Asphalt Soft** (`#2A2D31`, `--yard-asphalt-soft`): Declared in the token set as the raised dark surface step.
- **Steel** (`#868C93`, `--yard-steel`) and **Steel Dark** (`#4C5259`, `--yard-steel-dark`): Corrugated-container bodies, the crane line, the scrollbar thumb, ghost-button borders, section hairlines.
- **Rule** (`#C3C0B8`, `--yard-rule`): The 1px internal divider on paper surfaces.
- **Muted Ink** (`#565C63`, `--yard-ink-muted`): Secondary body copy and inactive nav labels (`5.3:1` on concrete).
- **On-Safety** (`#1B1D20`, `--yard-on-safety`): The black that sits on every orange and yellow fill.
- **Dark-surface support** (`gate-bg #24272B`, `gate-bg-hover #2E3237`, `gate-border #3A3F45`, `gate-arrow #8A9096`, `dark-muted #B6BAB4`, `dark-body #C4C7C2`, `log-note #9AA09A`, `log-code #C9CCC7`): The light-on-dark text and panel steps used inside the asphalt sections. These are set as literal values in the components, not tokens.

### Named Rules
**The Owned-Region Rule.** Safety orange fills whole regions — a container door, a CTA, the lead box, an active column. It is never a hairline underline or a 1px accent. Its mass is the point.

**The Black-on-Orange Rule.** Anything sitting on a safety-orange fill is asphalt `#1B1D20`, never white — the real safety-sign convention, and the only pairing that clears AA on the fill (`4.6:1`).

**The Two-Oranges Rule.** Orange as a fill is safety `#E4570F`; orange as text on concrete is rust `#A33D08`. Safety orange is never set as body text on the light ground.

**The Daylight Rule.** The page ground is concrete, never dark. Asphalt `#1B1D20` appears only as a bounded plate or a whole bounded section with its own edges.

## Typography

**Display Font:** Big Shoulders Display (with Archivo Narrow / Arial Narrow fallback)
**Body Font:** Archivo (with system-ui fallback)
**Label/Mono Font:** B612 Mono (with ui-monospace fallback)

**Character:** A condensed industrial signage voice over a neutral grotesque, with a monospace that carries every ID and measurement. The display face is tight, tall, and all-caps — it reads as stencilled yard signage rather than an editorial headline. Archivo stays quiet and legible underneath it. B612 Mono is the yard's clipboard: service IDs, versions, boot times, file paths, code.

### Hierarchy
- **Display** (800, `clamp(2.75rem, 6.6vw, 5.25rem)`, `0.92`, uppercase, `-0.02em`): The single `h1` only. "Firecracker microVMs with SwarmKit orchestration."
- **Headline** (800, `clamp(2.25rem, 5vw, 3.75rem)`, `0.95`, uppercase, `-0.01em`): Section `h2`s — How it works, Why SwarmCracker?, Quickstart, How it compares, Security, Documentation.
- **Title** (700, `1.625rem`–`2.5rem`, `1`–`1.05`, uppercase, `0.02em`): Card and plate `h3`s; the lead spec plate steps up to `clamp(1.75rem, 3vw, 2.25rem)`.
- **Body** (400, `1rem`, `1.6`): Base copy. Lead paragraphs run `1.0625rem` at `60ch`; supporting copy `0.9375rem` at `46–52ch`; the hero sub runs `34ch`.
- **Label** (700, `1.0625rem`, uppercase, `0.06em`): Buttons, nav links, rails, captions, table headers.
- **Mono** (400/700, `0.8125rem`–`0.9375rem`, tabular-nums, `0.02em`): Service IDs, version tags, spec references, boot measurements, terminal output, code blocks.

### Named Rules
**The One Display Face Rule.** Big Shoulders Display sets every heading and every uppercase label; Archivo never sets a heading, and the display face never sets body copy.

**The ID Rule.** Anything that is an identifier or a measurement — service IDs, version numbers, boot times, spec refs, file paths, code — is B612 Mono with tabular figures.

**The Uppercase Rule.** Display, headline, title, and label roles are uppercase by default; body copy is sentence case. Uppercase is the signage register, not a global treatment.

## Layout

A single centered column capped at `78rem`, with `1.5rem` side padding, holding stacked full-width sections. Section vertical rhythm is `clamp(3.5rem, 7vw, 6rem)`; the hero uses a slightly tighter `clamp(2.5rem, 6vw, 5rem)` top. The sticky header holds a `4rem` minimum height, and anchored sections carry `scroll-margin-top: 5rem` to clear it.

The hero is a two-column grid at `62rem` and up (`0.92fr / 1.08fr` — offer left, yard right); below that it stacks. How-it-works is a `1fr / 1fr` split at `62rem` (sequence left, placement log right). Security is a `1.05fr / 0.95fr` split at `58rem` (lead seal left, records right). The feature section is a 12-column board with a `0.75rem` gap whose plates deliberately span uneven widths — `7, 5, 5, 7, 6, 6` — so the board reads as a spec sheet rather than a uniform card grid; below `52rem` every plate spans all 12 columns.

Breakpoints in use: `40rem` (footer stack), `47.99rem` (diagram widens past viewport), `52rem` (plate spans activate), `58rem` (vault split), `62rem` (hero and how-it-works splits), and `720px` (desktop nav swaps to the `<details>` menu). Wide tables and diagrams are horizontally scrollable inside a focusable, bordered region rather than reflowing.

Measure is set per component rather than from a shared token: lead paragraphs `60ch`, supporting copy `46–52ch`, hero sub `34ch`.

> Note on declared tokens: `--yard-gutter` (`clamp(1.25rem, 5vw, 4rem)`) and `--yard-measure` (`68ch`) are declared in `tokens.css` but no component consumes them. The shipped layout uses a literal `1.5rem` side padding and per-component `ch` measures. The build is the source of truth here.

## Elevation & Depth

This system is flat. There are no cast shadows anywhere; depth is drawn with tonal layering and structural borders. Paper `#FBFAF7` plates sit on the concrete ground; concrete-deep `#DCD9D1` recesses cells inside paper (manifest sequence gutter, comparison header row); asphalt `#1B1D20` sections sit below the concrete as their own bounded planes. The one exception is the corrugated container box, which carries a two-line inset shadow to give the steel a top and bottom lip — a material cue, not a cast shadow.

The border vocabulary is the elevation system: `2px` solid asphalt for every component edge, `1px` rule `#C3C0B8` for internal dividers, `4px` asphalt for section breaks and `4px` safety for the docs section top, `6px` asphalt for the footer top. Gate cards respond to hover with a `-2px` translate and a border shift, not a shadow.

### Shadow Vocabulary
- **Container lip** (`box-shadow: inset 0 3px 0 rgba(255,255,255,0.18), inset 0 -3px 0 rgba(27,29,32,0.22)`): Only on corrugated container boxes; fakes the folded steel edge. The sole shadow in the system.

### Named Rules
**The Hard-Edge Rule.** Depth is tone and border, not blur. If a surface needs to separate from another, change its fill (concrete → paper → asphalt) or draw a border; do not add a drop shadow.

**The One Circle Rule.** Corners are square. The only rounded form in the system is the container seal, and it is a perfect circle.

## Shapes

The form language is stencilled steel and straight edges. Every corner is `0` radius; there is no softness anywhere. Components are bounded by `2px` solid asphalt outlines, and whole regions are separated by `4px` and `6px` hard rules. The recurring silhouettes are the corrugated container (steel body with a `90deg` repeating-linear-gradient corrugation and inset lip), the stencilled plate (dark rail over a paper body), the hazard kerb (a `-45deg` repeating-linear-gradient of asphalt and hazard yellow), and the gantry crane (open straight-line SVG in steel with an orange spreader). The single circle is the container seal — a ringed dot that reads as a locked latch. Icons are drawn as square-capped, mitered line SVGs on a 32-unit grid, matching the stencil geometry rather than a rounded icon set.

## Components

### Buttons
- **Shape:** Square, no radius (`0`), `2px` solid asphalt border.
- **Primary:** Safety-orange fill, asphalt text, display-caps label at `1.25rem` with `0.05em` tracking, padding `0.85rem 1.5rem`. Hover flips the fill to hazard yellow. Active presses down `1px`.
- **Hover / Focus:** Background/color transition `0.15s ease`; focus-visible draws a `3px` asphalt outline at `3px` offset.
- **Ghost:** Transparent fill, asphalt text, steel-dark border; hover inverts to an asphalt fill with paper text.
- **Copy buttons:** Two registers. On the hero container door, a small transparent button with a paper border on orange that fills hazard on hover/copy. In the quickstart manifest, a bordered button hidden until JS is present that fills asphalt on hover and safety on copy.
- **Nav CTA:** The safety tag — orange fill, asphalt text, `0.6rem 1.25rem`, hazard-yellow hover, `1px` press.

### Cards / Containers
- **Corner Style:** Square (`0`).
- **Spec plate:** Dark asphalt rail (mono hazard-yellow ref left, line icon right) over a paper body with an uppercase display title and muted body. The lead plate is wider and its title steps up.
- **Container box:** Steel fill with `90deg` corrugation and inset lip, `2px` asphalt border; mono stencilled ID left, display-caps seal state right. The lead box is safety orange.
- **Gate card:** Asphalt-soft panel with a `4px` hazard-yellow top border, a large safety-orange gate letter, a line arrow, and light-on-dark copy; hover raises `-2px` and shifts the border to safety orange.
- **Internal Padding:** `1.1rem–1.5rem` on bodies; rails `0.45rem–0.55rem`.

### Navigation
A sticky concrete header with a `1px` steel-dark bottom rule and a `4rem` minimum height. The brand is a stencilled mark: an orange square holding a corrugated-container line icon, the uppercase display wordmark, and a mono version ID behind a `1px` rule. Desktop links are uppercase display caps in muted ink with a `3px` safety-orange bottom border on hover; the CTA is the safety tag. Below `720px` the links collapse into a `<details>/<summary>` menu (no JavaScript) that opens an asphalt panel with a `4px` safety bottom border and paper display-caps links that turn hazard yellow on hover. A skip link is the first focusable element.

### Manifest / Log (signature)
Two paired records of the same work. The **placement log** is an asphalt terminal panel with a hazard-yellow header (`Placement log` / `swarmkit / executor`), mono output lines that fade-and-slide in on a `0.35s` stagger, a hazard-yellow command line, a bold converged line, and a muted illustrative-output note. The **load-order manifest** is a paper panel with an asphalt rail, a numbered sequence gutter on concrete-deep, and seven bordered mono command blocks with copy buttons. Both are explicitly labelled illustrative.

### Comparison Table
A bordered paper panel with an asphalt display-caps caption. Header cells sit on concrete-deep; the SwarmCracker column header is a safety-orange fill with asphalt text. The SwarmCracker body column carries a `safety-wash` tint (`rgba(228,87,15,0.09)`, deepening to `0.16` on row hover) and asphalt text — the only place orange is used at low opacity.

### Gantry Crane & Yard (signature)
The hero's memorable moment. An SVG gantry crane in steel spans the top of the yard; a trolley traverses in from the left, a safety-orange spreader lowers, and the lead container drops onto the stack. Three corrugated boxes — each a service replica with a stencilled mono ID and a sealed state — sit right, over a hazard kerb. The whole sequence is `aria-hidden`-free but decorative in role, with an accessible label describing the placement.

### Footer
An asphalt plane under a `6px` asphalt top border, led by a full-width hazard kerb. Uppercase display links at `44px` minimum touch height, paper/hazard hover and focus, and a mono copyright line.

## Do's and Don'ts

### Do:
- **Do** keep the page ground concrete `#E8E6E0`. It is the world's daylight premise.
- **Do** give safety orange `#E4570F` whole regions — a CTA, a container door, a lead box, a column — rather than hairlines.
- **Do** set text and icons on orange or yellow fills to asphalt `#1B1D20`, per the Black-on-Orange Rule.
- **Do** use rust `#A33D08` when orange text must sit on the light concrete ground.
- **Do** separate surfaces with `2px` asphalt borders, `1px` rule dividers, and tone shifts, per the Hard-Edge Rule.
- **Do** set every ID, version, measurement, and code string in B612 Mono with tabular figures.
- **Do** keep the feature board uneven (`7, 5, 5, 7, 6, 6`) so it reads as a spec sheet, not a card grid.
- **Do** keep motion to the single crane placement and the log stagger, and honor `prefers-reduced-motion` (globally in `tokens.css`, plus per-component overrides in Hero, Terminal, and DocsGateway).

### Don't:
- **Don't** reintroduce a dark near-black page background or the incumbent `#FF6B35` accent; the world replaced that direction on purpose (see PRODUCT.md).
- **Don't** set safety orange `#E4570F` as body text on concrete — it is a fill color; rust is the light-ground text orange.
- **Don't** round a corner. `0` radius is the form language; only the container seal is a circle.
- **Don't** add a drop shadow. Depth is tone and border.
- **Don't** set white text on an orange or yellow fill.
- **Don't** let Archivo set a heading or Big Shoulders set body copy.
- **Don't** flatten the components into a uniform grid of identical cards — uneven spans and distinct plate/box/gate families are the point.
- **Don't** fabricate logos, testimonials, benchmarks, or live-host output; demonstration data stays labelled illustrative.
