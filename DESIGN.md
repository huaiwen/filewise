---
name: Filewise website
description: "A monochrome, demonstration-first website for a local file workspace."
colors:
  ink: "#171717"
  muted: "#646464"
  paper: "#f8f8f8"
  white: "#ffffff"
  wash: "#efefef"
  line: "#dedede"
  green: "#236949"
  green-wash: "#eaf3ed"
  red: "#8d3f32"
  red-wash: "#fbefed"
typography:
  display:
    fontFamily: "Barlow Condensed, sans-serif"
    fontSize: "clamp(62px, 5.3vw, 78px)"
    fontWeight: 300
    lineHeight: 1.12
    letterSpacing: "-0.025em"
  display-zh:
    fontFamily: "Filewise Headings SC, PingFang SC, Microsoft YaHei, sans-serif"
    fontSize: "clamp(40px, 4.05vw, 58px)"
    fontWeight: 300
    lineHeight: 1.42
    letterSpacing: "-0.035em"
  headline:
    fontFamily: "Barlow Condensed, sans-serif"
    fontSize: "clamp(42px, 4.4vw, 64px)"
    fontWeight: 300
    lineHeight: 1.12
  title:
    fontSize: "19px"
    fontWeight: 550
    letterSpacing: "-0.02em"
  body:
    fontFamily: "Avenir Next, Segoe UI, PingFang SC, Microsoft YaHei, sans-serif"
    fontSize: "16px"
    lineHeight: 1.85
  code:
    fontFamily: "SFMono-Regular, Consolas, Liberation Mono, monospace"
    fontSize: "13px"
    lineHeight: 2.25
rounded:
  control: "3px"
  document: "4px"
  conversation: "6px"
  segment: "24px"
spacing:
  compact: "12px"
  standard: "24px"
  desktop-section: "112px"
  mobile-section: "59px"
components:
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.white}"
    rounded: "{rounded.control}"
    padding: "11px 22px"
  button-primary-hover:
    backgroundColor: "#383838"
    textColor: "{colors.white}"
  document:
    backgroundColor: "{colors.white}"
    rounded: "{rounded.document}"
---

# Design System: Filewise website

## Overview

**Creative North Star: "The visible version"**

This system applies to `site/`, not the local Go workbench. The owner approved a visual direction close to Impeccable's public website: a neutral field, light display typography, a substantial working example, and a clear black installation action. The previous green-card identity is retired for this surface.

**Key Characteristics:**
- Open whitespace around compact, information-bearing document surfaces.
- Black and gray for hierarchy; green reserved for evidence, selection and additions.
- Self-hosted display typography, restrained controls, no automatic demonstration cycling.

## Colors

The page stays achromatic; colored text identifies a meaningful state.

### Primary

Green marks evidence, active selections, focus and added content. Addition and removal washes support actual difference rows, paired with plus/minus signs so color is not the only cue.

### Neutral

Paper is the page ground, white identifies document and command surfaces, ink carries headings and actions, muted carries secondary information, and line separates records. The agent region uses a slightly darker neutral field (`#f0f0f0`).

**The Evidence Accent Rule.** Green conveys a source or state; it does not fill decorative marketing cards.

## Typography

Latin display uses the self-hosted Barlow Condensed Light. Chinese display uses a self-hosted Noto Sans SC Light subset, named Filewise Headings SC in CSS. The subset covers the current headings; new Chinese headings require a regenerated subset or use the documented system fallback. Font sources and licenses are recorded in `site/fonts/README.md`.

Body copy uses the local sans-serif stack. The Word specimen uses Georgia / Songti SC / SimSun, with a document title, section heading and sentence-level diff. Monospace is for commands, hashes and locators. The brand wordmark is body typography, not a separate display face.

The frontmatter records desktop base roles. At 820px the layout stacks; the English hero becomes 68px, then a fluid 49–60px at 450px. Chinese hero type becomes a fluid 36–48px, then 30–39px. Section headings remain lighter than feature titles. Main copy is 14–16px; secondary notes are 12px; compact metadata has an 11px floor. Word text is 13–14px, the specimen title 17–18px, semantic-meta copy 13px, and command text 11–12px.

**The Readable Record Rule.** Preserve readable metadata and real locators when space gets tight; wrap rather than shrinking functional text below 11px.

## Layout

The desktop container is at most 1288px, with 48px minimum side margins. Margins become 32px at 1100px, 22px at 820px and 18px at 450px. The desktop hero pairs the demonstration on the left with copy/action on the right; mobile puts copy/action first. The DOM keeps that meaningful reading order.

Large sections use 112px block spacing, reduced to 72px at tablet width and 59px on narrow screens. Supporting regions alternate density: document records and explanatory rows, a conversation on a neutral field, then installation and expandable questions. Features are separated rows, not a grid of identical cards.

## Elevation & Depth

Documents use a quiet `0 12px 30px #00000007` shadow; the selected demonstration tab uses `0 2px 6px #00000010`. A slightly rotated rear sheet represents a prior version. Other regions are flat, separated by borders or tonal fields. No glass, glow or fake paper texture.

## Shapes

Document and command controls have small radii. A capsule is reserved for the grouped demonstration tabs and the compact GitHub link. Original outline SVG icons share rounded joins and a consistent 1.5px stroke; diff signs are data, not pictogram substitutes.

## Components

### Actions and navigation

Primary actions are black with white text; secondary actions are plain text or outlined buttons. Focus uses a 2px green outline with 5px offset. Navigation keeps installation and language switching on mobile. Text selection uses `#cde4d5` with ink text; scrollbars retain native behavior with a neutral palette.

### Demonstration

Three keyboard-navigable tabs expose files, version differences and evidence. The example is a real, downloadable Word pair: paragraph 3 changes from 100 to 120 kPa. Short file hashes match the original bytes; this is a text illustration, not Word layout reproduction. A separated `META / CHANGE MEANING` region states the quantity change and distinguishes rule-derived facts from unevaluated downstream impact. The specimen's desktop minimum height is 425px. The version selector is a native two-state range: a 2px neutral rail, outlined green thumb, visible endpoint labels and localized aria-valuetext. It is disabled until enhancement loads and hidden from interaction when another demonstration tab is selected.

Panels settle once over 180ms using `cubic-bezier(.16, 1, .3, 1)`. Content is visible by default; reduced motion disables animation, transitions and smooth scrolling. There is no timer-driven cycling.

### Installation

Provider tabs use an underline rather than capsule styling. Long commands wrap. Copy success and manual-selection fallback use a polite status region. Unpublished npm commands retain their notice and have no copy button. These are product-state constraints, not decoration.

### Questions

Native `details` and `summary` provide disclosure, keyboard behavior and focus without another JavaScript component.

## Do's and Don'ts

- Do use the document's actual content, version and locator as the visual proof.
- Do preserve bilingual layout, native controls, visible focus and reduced-motion behavior.
- Do keep hosted fonts, license files and the static allowlist together at deployment.
- Don't reintroduce green marketing-card grids, eyebrow labels or ornamental statistics.
- Don't invent testimonials, platform endorsements, online workspace behavior or a published npm package.
- Don't apply this website's redesign to `internal/workspace/ui/` without a separate request.
