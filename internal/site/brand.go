package site

import (
	"fmt"
	"html"
	"strings"
)

// The brand palette. One declaration feeds every generated asset and the
// stylesheet, so a colour cannot drift between the mark and the page.
//
// These are the light primitives: the assets draw with them and the semantic
// token table below starts from them. The dark values live in the table only;
// the mark and the banner keep their own ink surface in both themes.
const (
	ink   = "#0b191a"
	ink2  = "#12282a"
	paper = "#f4efe5"
	card  = "#fffdf7"
	teal  = "#0f6b5f"
	mint  = "#8fe3c8"
	amber = "#d98a4a"
	// amberInk is the same copper at text contrast on the paper and card
	// surfaces; the bright amber is reserved for the dark panels.
	amberInk = "#9c5a24"
	sand     = "#cfc4ae"
	muted    = "#5f706c"
	mist     = "#9db3ae"
	rule     = "#ded4c3"
	// mintWash is the guarantee panel and washRule is its border.
	mintWash = "#e4f3ec"
	washRule = "#c9e6d9"
	// panelRule is a translucent paper hairline over a dark panel, so one
	// rule serves the masthead, the menu and every panel.
	panelRule     = "rgba(244, 239, 229, 0.16)"
	panelRuleDark = "rgba(244, 239, 229, 0.22)"

	// The two hero glows and the three surface shadows.
	heroGlowMint  = "rgba(143, 227, 200, 0.17)"
	heroGlowAmber = "rgba(217, 138, 74, 0.1)"
	shadowSm      = "0 8px 24px rgba(3, 10, 10, 0.07)"
	shadowMd      = "0 18px 50px rgba(3, 10, 10, 0.12)"
	shadowLg      = "0 40px 100px rgba(3, 10, 10, 0.4)"

	// The dark palette. The page is the darkest surface; panels and cards are
	// raised above it, one step lighter each, so a surface reads without
	// depending on a border.
	darkPage        = "#0d1517"
	darkPageInk     = "#ece7dd"
	darkPanel       = "#16262a"
	darkPanelRaised = "#1d3034"
	darkCard        = "#1c2c2f"
	darkMuted       = "#a7b7b2"
	darkRule        = "#2e3e41"
	darkTeal        = "#7ed9c1"
	darkAmberInk    = "#e2a26d"
	darkChip        = "#2e4043"
	darkCode        = "#2a3b3e"
	darkCodeInk     = "#f0ece2"
	darkCodeBlock   = "#1b2b2e"
	darkPanelCode   = "#0f1e21"
	darkWash        = "#143431"
	darkWashRule    = "#2a5a52"

	serif = "Georgia, 'Times New Roman', serif"
	sans  = "'Segoe UI', -apple-system, BlinkMacSystemFont, Roboto, 'Helvetica Neue', Arial, sans-serif"
	mono  = "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
)

// paletteToken is one semantic colour in both themes. The stylesheet renders
// the table once per palette, and the contrast test reads the same table, so
// a shipped value and its checked value cannot drift apart.
type paletteToken struct {
	Token string
	Light string
	Dark  string
}

// themeTokens is the one semantic palette. Surfaces and text are split by
// role rather than by the primitive they started from: the light look is the
// one that shipped, the dark look raises the page, the panels and the cards
// on three steps of the same deep green.
var themeTokens = []paletteToken{
	{"page", paper, darkPage},
	{"page-ink", ink, darkPageInk},
	{"panel", ink, darkPanel},
	{"panel-raised", ink2, darkPanelRaised},
	{"panel-ink", paper, paper},
	{"panel-muted", mist, mist},
	{"panel-quiet", sand, sand},
	{"panel-rule", panelRule, panelRuleDark},
	{"card", card, darkCard},
	{"muted", muted, darkMuted},
	{"rule", rule, darkRule},
	{"teal", teal, darkTeal},
	{"teal-ink", paper, ink},
	{"amber-ink", amberInk, darkAmberInk},
	{"chip", ink, darkChip},
	{"chip-ink", paper, paper},
	{"code", sand, darkCode},
	{"code-ink", ink, darkCodeInk},
	{"code-block", ink, darkCodeBlock},
	{"code-block-ink", paper, darkCodeInk},
	{"panel-code", paper, darkPanelCode},
	{"panel-code-ink", ink, darkCodeInk},
	{"wash", mintWash, darkWash},
	{"wash-rule", washRule, darkWashRule},
	{"accent-ink", ink, ink},
	{"mint", mint, mint},
	{"amber", amber, amber},
}

// markShapes writes the 120 x 120 mark. The caller places it in a group.
func markShapes(out *strings.Builder, indent string) {
	fmt.Fprintf(out, "%s<rect width=\"120\" height=\"120\" rx=\"28\" fill=\"%s\"/>\n", indent, ink)
	fmt.Fprintf(out, "%s<rect x=\"18\" y=\"34\" width=\"84\" height=\"56\" rx=\"9\" fill=\"%s\"/>\n", indent, paper)
	fmt.Fprintf(out, "%s<path d=\"M21 40 L60 66 L99 40\" fill=\"none\" stroke=\"%s\" stroke-width=\"4\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>\n", indent, ink)
	fmt.Fprintf(out, "%s<circle cx=\"95\" cy=\"37\" r=\"7\" fill=\"%s\"/>\n", indent, amber)
	fmt.Fprintf(out, "%s<rect x=\"29\" y=\"70\" width=\"54\" height=\"5\" rx=\"2.5\" fill=\"%s\"/>\n", indent, mint)
	fmt.Fprintf(out, "%s<rect x=\"29\" y=\"79\" width=\"39\" height=\"5\" rx=\"2.5\" fill=\"%s\"/>\n", indent, amber)
	fmt.Fprintf(out, "%s<rect x=\"29\" y=\"88\" width=\"24\" height=\"5\" rx=\"2.5\" fill=\"%s\"/>\n", indent, sand)
}

// Mark renders the square product mark, suitable for a favicon or a small
// avatar.
func (s Site) Mark() []byte {
	var out strings.Builder
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	out.WriteString("<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 120 120\" role=\"img\" aria-labelledby=\"mailsense-mark-title mailsense-mark-desc\">\n")
	fmt.Fprintf(&out, "  <title id=\"mailsense-mark-title\">%s</title>\n", html.EscapeString(s.Product))
	out.WriteString("  <desc id=\"mailsense-mark-desc\">An envelope holding three ranked lines, with an amber marker at its corner.</desc>\n")
	markShapes(&out, "  ")
	out.WriteString("</svg>\n")
	return []byte(out.String())
}

// Logo renders the horizontal lockup: mark and wordmark. It has no language,
// so the same file serves both trees. The wordmark follows the visitor's
// colour scheme from the embedded style block, so the lockup stays legible on
// a dark surface instead of vanishing into it; the mark keeps its own tile.
func (s Site) Logo() []byte {
	var out strings.Builder
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	fmt.Fprintf(&out, "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 400 120\" role=\"img\" color=\"%s\" aria-labelledby=\"mailsense-logo-title mailsense-logo-desc\">\n", ink)
	fmt.Fprintf(&out, "  <title id=\"mailsense-logo-title\">%s</title>\n", html.EscapeString(s.Product))
	out.WriteString("  <desc id=\"mailsense-logo-desc\">The square mark beside the Mailsense wordmark.</desc>\n")
	// The wordmark reads currentColor, so an inline copy follows the
	// surrounding text; as an image it falls back to the ink above and
	// switches to paper when the viewer prefers a dark scheme.
	out.WriteString("  <style>\n")
	out.WriteString("    .wordmark { fill: currentColor; }\n")
	fmt.Fprintf(&out, "    .tagline { fill: %s; }\n", teal)
	out.WriteString("    @media (prefers-color-scheme: dark) {\n")
	fmt.Fprintf(&out, "      .wordmark { fill: %s; }\n", paper)
	fmt.Fprintf(&out, "      .tagline { fill: %s; }\n", mint)
	out.WriteString("    }\n")
	out.WriteString("  </style>\n")
	out.WriteString("  <g transform=\"translate(4 4) scale(0.92)\">\n")
	markShapes(&out, "    ")
	out.WriteString("  </g>\n")
	fmt.Fprintf(&out, "  <text class=\"wordmark\" x=\"132\" y=\"66\" font-family=\"%s\" font-size=\"46\" letter-spacing=\"-1\">%s</text>\n", serif, html.EscapeString(s.Product))
	fmt.Fprintf(&out, "  <text class=\"tagline\" x=\"134\" y=\"92\" font-family=\"%s\" font-size=\"12\" letter-spacing=\"2.6\">MAIL TRIAGE</text>\n", mono)
	fmt.Fprintf(&out, "  <rect x=\"134\" y=\"100\" width=\"44\" height=\"3\" fill=\"%s\"/>\n", amber)
	out.WriteString("</svg>\n")
	return []byte(out.String())
}

// Banner renders the social and documentation banner in one language. The
// tagline comes from the same localized fact as the landing page.
func (s Site) Banner(lang string) []byte {
	banner := s.Hero.Banner.For(lang)
	first, second := banner, ""
	if index := strings.Index(banner, ". "); index >= 0 {
		first = banner[:index+1]
		second = banner[index+2:]
	}
	var out strings.Builder
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	out.WriteString("<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 1200 630\" role=\"img\" aria-labelledby=\"mailsense-banner-title mailsense-banner-desc\">\n")
	fmt.Fprintf(&out, "  <title id=\"mailsense-banner-title\">%s</title>\n", html.EscapeString(s.Product))
	fmt.Fprintf(&out, "  <desc id=\"mailsense-banner-desc\">%s</desc>\n", html.EscapeString(banner))
	fmt.Fprintf(&out, "  <rect width=\"1200\" height=\"630\" fill=\"%s\"/>\n", ink)
	fmt.Fprintf(&out, "  <rect width=\"1200\" height=\"10\" fill=\"%s\"/>\n", mint)
	fmt.Fprintf(&out, "  <rect x=\"64\" y=\"78\" width=\"190\" height=\"190\" rx=\"46\" fill=\"%s\"/>\n", paper)
	out.WriteString("  <g transform=\"translate(72 86) scale(1.45)\">\n")
	markShapes(&out, "    ")
	out.WriteString("  </g>\n")
	fmt.Fprintf(&out, "  <text x=\"300\" y=\"168\" font-family=\"%s\" font-size=\"17\" letter-spacing=\"3.2\" fill=\"%s\">%s</text>\n", mono, mint, html.EscapeString(strings.ToUpper(s.Hero.Eyebrow.For(lang))))
	fmt.Fprintf(&out, "  <text x=\"300\" y=\"248\" font-family=\"%s\" font-size=\"88\" letter-spacing=\"-2\" fill=\"%s\">%s</text>\n", serif, paper, html.EscapeString(s.Product))
	fmt.Fprintf(&out, "  <text x=\"304\" y=\"318\" font-family=\"%s\" font-size=\"34\" fill=\"%s\">%s</text>\n", serif, paper, html.EscapeString(first))
	if second != "" {
		fmt.Fprintf(&out, "  <text x=\"304\" y=\"360\" font-family=\"%s\" font-size=\"34\" fill=\"%s\">%s</text>\n", serif, paper, html.EscapeString(second))
	}
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"392\" width=\"120\" height=\"4\" fill=\"%s\"/>\n", amber)
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"424\" width=\"520\" height=\"18\" rx=\"9\" fill=\"%s\"/>\n", mint)
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"456\" width=\"380\" height=\"18\" rx=\"9\" fill=\"%s\"/>\n", amber)
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"488\" width=\"240\" height=\"18\" rx=\"9\" fill=\"%s\"/>\n", sand)
	repo := strings.TrimPrefix(s.Repository, "https://")
	fmt.Fprintf(&out, "  <text x=\"72\" y=\"580\" font-family=\"%s\" font-size=\"16\" fill=\"%s\">%s · %s</text>\n", mono, mist, LicenseID, html.EscapeString(repo))
	out.WriteString("</svg>\n")
	return []byte(out.String())
}
