# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

People who run their working life from a classic Outlook profile on Windows and
already have years of mail in it. They are comfortable installing their own
software, they care where their mail goes, and they evaluate a tool by watching
it work rather than reading a claim.

## Product Purpose

Mailsense turns a local Outlook mailbox into answers and a ranked queue. Ask
answers a plain-language question with a computed count or list, matching
messages, or a written answer that cites the messages it used. The store, the
index, the classifier and the learned profile stay on the machine.

Success for the public site: a visitor understands the Ask mechanism, believes
the local boundary, and installs the Windows build.

## Positioning

The product mechanism a neighbouring tool could not truthfully copy is the
combination of local computation and grounded writing: a count, a ranking or a
company profile comes from a typed query with no model call, and a written
answer is accepted only when every claim points at a retrieved message. Every
citation opens the message it names.

## Operating Context

- The live source is the classic Outlook profile already open on Windows, read
  through COM. The connector is read only.
- The release ships one Windows installer. The install is per user, needs no
  administrator, and keeps the data directory if the product is uninstalled.
- The source builds on Linux as a preview with an empty inbox. That build is not
  a release download.
- Ask has three modes: Auto, Local and Remote. Local never calls a model.
- Triage needs a configured decider. Ask in Local mode does not.
- The public site is static, generated from `docs/site.json`, built by Jekyll
  and served by GitHub Pages at https://llm-4-people.github.io/mailsense/.

## Capabilities and Constraints

- Answers are bounded by `qa.max_messages`, `qa.retrieval_timeout`,
  `qa.call_timeout` and the budget settings. The planner sees at most six prior
  turns of one thread.
- A written answer is asked for at most 120 words, must mark every claim with a
  message number, and is refused when a marker points outside the retrieved set.
- Citations are renumbered to citation positions and kept only when they name a
  real message.
- There is no telemetry, no analytics and no vendor account. Five request
  kinds exist, including the update check; the privacy page lists them.
- The public copy exists in English and French with identical structure. The
  browser language picks the tree.
- No real mail, client name, machine name, vendor name or environment path
  appears in any public asset.

## Brand Commitments

- Name: Mailsense.
- Voice: plain, sentence case, no em dash, no spaced en dash, no double hyphen
  punctuation, no stock phrases.
- Palette: ink `#0b191a`, ink-2 `#12282a`, paper `#f4efe5`, card `#fffdf7`,
  teal `#0f6b5f`, mint `#8fe3c8`, amber `#d98a4a`, sand `#cfc4ae`,
  muted `#5f706c`, mist `#9db3ae`, rule `#ded4c3`.
- Type: Georgia for display, the system UI stack for body, the system mono stack
  for labels. No external font request.
- Mark: a dark rounded square holding a paper envelope, three ranked lines in
  teal, amber and muted, and an amber dot at the corner.
- The site is static and makes no request to another origin.

## Evidence on Hand

- Eight product screens captured from the Linux preview in both languages,
  using an invented mailbox. The cast (Atelier Nord, Studio Lumen, Sam Rivera,
  Morgan Ellis, Northwind) is invented and appears nowhere else.
- A 1200 by 630 social preview rendered from the same banner.
- No customer, benchmark, price, press item, testimonial or endorsement exists.
  Future work must not invent one.

## Product Principles

1. Show the mechanism, not an adjective. Every claim on the page names how it
   works.
2. Ask leads. The ranked queue is the second proof, never the first.
3. State the boundary plainly. What leaves the machine is a feature of the
   design, not a footnote.
4. Respect the visitor's time and screen. Dense, aligned information beats
   repeated cards and stacked full-width images.
5. Keep both languages equal in structure and in claim.

## Accessibility & Inclusion

Target WCAG 2.2 AA. Keyboard reachable navigation and controls, visible focus,
contrast checked in both palettes, no motion required to read, and no meaning
carried by colour alone.
