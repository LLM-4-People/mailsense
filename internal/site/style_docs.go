package site

import "strings"

// writeDocumentationCSS writes the documentation frame: breadcrumb, reading
// measure, index pills, rail and compact rail. The landing sections stay in
// style.go; the two surfaces share only the tokens and base rules.
func writeDocumentationCSS(out *strings.Builder) {
	out.WriteString(`/* Documentation pages */

.doc-head { max-width: 1280px; padding-top: clamp(28px, 4vw, 52px); }

.breadcrumb {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px;
  color: var(--muted);
  font-family: var(--mono);
  font-size: 0.58rem;
  letter-spacing: 0.1em;
  text-transform: uppercase;
}
.breadcrumb a { color: var(--muted); text-decoration: none; }
.breadcrumb a:hover { color: var(--teal); }
.breadcrumb span[aria-current] { color: var(--ink); }

.doc-layout {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(210px, 270px);
  column-gap: clamp(32px, 4vw, 56px);
  row-gap: 0;
  align-items: start;
  max-width: 1280px;
  margin-inline: auto;
  padding-bottom: clamp(40px, 5vw, 80px);
}
.doc-main { display: contents; min-width: 0; }
.doc-main > * { grid-column: 1; min-width: 0; }
.doc-main > .doc-index-wrap { grid-column: 1 / -1; grid-row: 2; }
.doc-rail {
  grid-column: 2;
  /* The rail begins below the full-width index and spans the remaining
     implicit rows, so its height never widens the title row. */
  grid-row: 3 / span 1000;
  align-self: start;
  width: 100%;
  min-width: 0;
  position: sticky;
  top: calc(var(--masthead-h) + var(--doc-index-h, 0px) + 18px);
  padding: 20px;
  border: 1px solid var(--rule);
  border-radius: var(--r-lg);
  background: var(--card);
  box-shadow: var(--shadow-sm);
}
.doc-rail-compact { display: none; }

.doc-main > h1 {
  margin: 18px 0;
  max-width: 22ch;
  font-family: var(--serif);
  font-size: clamp(2.4rem, 4.6vw, 4rem);
  line-height: 1.02;
  letter-spacing: -0.04em;
}
.doc-main > h2 {
  margin: 2.2em 0 0.5em;
  font-family: var(--serif);
  font-size: clamp(1.6rem, 2.4vw, 2.2rem);
  line-height: 1.1;
  letter-spacing: -0.03em;
}
.doc-main > h3 { margin: 1.8em 0 0.5em; font-size: 1.05rem; font-weight: 650; }
.doc-main > p,
.doc-main > ul,
.doc-main > ol,
.doc-main > blockquote,
.doc-main > h1,
.doc-main > h2,
.doc-main > h3,
.doc-main > h4,
.doc-main > h5,
.doc-main > h6 { max-width: var(--measure); }
.doc-main > h1,
.doc-main > h2,
.doc-main > h3,
.doc-main > h4,
.doc-main > h5,
.doc-main > h6 { scroll-margin-top: calc(var(--doc-index-h, 0px) + 8px); }
.doc-main > table,
.doc-main > pre,
.doc-main > .doc-index-wrap { max-width: 100%; }
.doc-main > p { margin: 0 0 1.1em; }
.doc-main > ul,
.doc-main > ol { margin: 0 0 1.2em; padding-left: 1.3rem; }
.doc-main > li { max-width: var(--measure); margin: 0.35rem 0; }
.doc-main > table { width: 100%; border-collapse: collapse; margin: 1.4rem 0; font-size: 0.88rem; }
.doc-main th {
  padding: 9px 10px 9px 0;
  text-align: left;
  vertical-align: bottom;
  border-bottom: 1px solid var(--ink);
  color: var(--muted);
  font-family: var(--mono);
  font-size: 0.56rem;
  letter-spacing: 0.1em;
  text-transform: uppercase;
}
.doc-main td { padding: 10px 10px 10px 0; vertical-align: top; border-bottom: 1px solid var(--rule); overflow-wrap: anywhere; }
.doc-main table td:first-child,
.doc-main table th:first-child { overflow-wrap: break-word; }
.doc-main tr:last-child td { border-bottom: 0; }
.doc-main pre {
  max-width: 100%;
  padding: 16px 18px;
  border-radius: var(--r-md);
  background: var(--ink);
  color: var(--paper);
  overflow-x: auto;
  line-height: 1.5;
}
.doc-main pre code { background: none; padding: 0; color: inherit; font-size: 0.8rem; white-space: pre; }
.doc-main code {
  padding: 0.12em 0.32em;
  border-radius: 6px;
  background: var(--sand);
  color: var(--ink);
  font-family: var(--mono);
  font-size: 0.86em;
  overflow-wrap: anywhere;
}
.doc-main blockquote {
  max-width: var(--measure);
  margin: 1.4rem 0;
  padding: 14px 18px;
  border-radius: var(--r-md);
  background: var(--card);
  border: 1px solid var(--rule);
  color: var(--muted);
  font-style: italic;
}
.doc-main hr { max-width: var(--measure); border: 0; border-top: 1px solid var(--rule); margin: 2.2rem 0; }
.doc-main img { max-width: 100%; height: auto; margin: 1.2rem 0; border-radius: var(--r-md); }
.doc-main > .doc-meta {
  max-width: var(--measure);
  margin-top: 2.4rem;
  padding-top: 12px;
  border-top: 1px solid var(--rule);
  color: var(--muted);
  font-family: var(--mono);
  font-size: 0.6rem;
  letter-spacing: 0.02em;
}
.doc-main > .doc-meta a { color: var(--muted); text-decoration: none; }
.doc-main > .doc-meta a:hover { color: var(--teal); }

.doc-index-wrap {
  position: sticky;
  top: calc(var(--masthead-h) + 8px);
  z-index: 30;
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 10px 0 30px;
  padding: 6px;
  border: 1px solid var(--rule);
  border-radius: 18px;
  background: var(--card);
  box-shadow: var(--shadow-sm);
}
.doc-index {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  padding: 0;
  overflow: visible;
}
.doc-index > * { flex: 0 0 auto; }
.doc-index-hint {
  display: none;
  flex: 0 0 24px;
  position: relative;
  align-self: stretch;
  border-radius: 12px;
  background: linear-gradient(to right, transparent, var(--card) 55%);
}
.doc-index-hint::after {
  content: "›";
  position: absolute;
  right: 3px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--muted);
  font-family: var(--mono);
  font-size: 0.9rem;
  line-height: 1;
}
.doc-index-wrap.is-scrollable .doc-index-hint { display: block; }
.doc-index a,
.doc-index-current {
  padding: 8px 11px;
  border-radius: var(--r-pill);
  color: var(--muted);
  font-family: var(--mono);
  font-size: 0.68rem;
  letter-spacing: 0.08em;
  text-decoration: none;
  text-transform: uppercase;
  white-space: nowrap;
}
.doc-index a:hover { background: var(--paper); color: var(--ink); }
.doc-index-current { background: var(--ink); color: var(--paper); }

.rail-title {
  margin: 0 0 10px;
  color: var(--teal);
  font-family: var(--mono);
  font-size: 0.56rem;
  letter-spacing: 0.12em;
  text-transform: uppercase;
}
.rail-list { list-style: none; margin: 0; padding: 0; }
.rail-list a {
  display: block;
  padding: 7px 0;
  border-bottom: 1px solid var(--rule);
  color: var(--muted);
  font-size: 0.78rem;
  line-height: 1.35;
  text-decoration: none;
}
.rail-list a:hover { color: var(--teal); }
.rail-fallback {
  margin: 14px 0 0;
  color: var(--muted);
  font-family: var(--mono);
  font-size: 0.56rem;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
.rail-fallback a { color: var(--muted); text-decoration: none; }
.rail-fallback a:hover { color: var(--teal); }

.doc-rail-compact {
  margin: 10px 0 18px;
  padding: 4px 16px;
  border: 1px solid var(--rule);
  border-radius: 16px;
  background: var(--card);
}
.doc-rail-compact summary {
  padding: 10px 0;
  list-style: none;
  cursor: pointer;
  color: var(--ink);
  font-family: var(--mono);
  font-size: 0.66rem;
  letter-spacing: 0.1em;
  text-transform: uppercase;
}
.doc-rail-compact summary::-webkit-details-marker { display: none; }
.doc-rail-compact summary::after { content: " +"; color: var(--teal); }
.doc-rail-compact[open] summary::after { content: " -"; }
.doc-rail-compact .rail-list { padding-bottom: 10px; }

`)
}
