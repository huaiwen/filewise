---
version: 1
slug: "site-index-html"
primary_target: "site/index.html"
related_targets: ["site/en/index.html","site/style.css","site/app.js"]
---

# Website redesign brief

Scope: `site/index.html`, `site/en/index.html`, their shared assets and tests. Visitor mode: Persuade. The owner confirmed a visual direction close to Impeccable's public website, not merely its information architecture. Keep the bilingual product facts, release status, installation paths and access boundaries. The local workbench and existing installer remain unchanged. Follow-up owner direction: demonstrate Word rather than JSON, show the meaning of changes in meta, and move primary development to Go. Keep release-vs-development status explicit.

## Direction contract

**THESIS:** Let visitors inspect a change before asking them to install. Replace the green-card landing page with a spacious monochrome demonstration. A before/after document comparison carries the first viewport.

**OWN-WORLD:** Neutral `#f8f8f8`, near-black `#171717`, white document surfaces, thin gray rules, small `#236949` evidence accents. Light, condensed Latin display typography and light CJK headings. Compact, square-ended controls; rounded segmented tabs only where they function as a group.

**STORY:** A Word requirement paragraph changes from 100 to 120 kPa. Filewise records the originals and stores the rule-derived quantity change in meta with both file hashes, quotations and paragraph locations. The downloadable synthetic Word files ground the demonstration; arbitrary prose or downstream impact stays marked for review. Continue to folder organization, an agent conversation and the installation selector.

**FIRST VIEWPORT:** A 76px navigation strip. At desktop, a substantial interactive document demonstration on the left and a two-line 76px Latin / 58px Chinese headline on the right; black installation action directly below concise product copy. A quiet compatibility row closes the viewport. On mobile, headline/action precedes the full-width demonstration. Signature interaction: a keyboard-accessible native range reveals the two document versions; existing tabs switch files, comparison and citation. One 180ms panel settle, no automatic cycling.

**FORM:** User-pinned Impeccable-like demonstration-first landing page. Official seed `757b7c46` was run; the user's explicit choice overrides its random index and unrelated catalog challengers. No new style-choice round. Code-led; no generated comp or photographic assets. Fonts are self-hosted with licenses and source records.

**FINISH:** unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

## Validation and authority

Desktop/mobile screenshots in both languages, narrow-width layouts, range pointer/keyboard input, tab keyboard/copy controls, no-JS fallback, reduced motion, local-only resources, axe and the official mechanical detector. The original redesign's official detector pass is historical; the Word/meta follow-up is checked with current tests and browser captures, not represented as a new full official workflow run. Review is performed by the parent: the owner authorized redesign, not delegated agents. Do not describe this as independent review or as executing every official workflow handoff. The design phase made no commit, publication or deployment; subsequent source pushes require separate owner authorization.
