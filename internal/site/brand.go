package site

import (
	"fmt"
	"html"
	"strings"
)

// The brand palette. One declaration feeds every generated asset.
const (
	ink   = "#17242c"
	cream = "#f3ead9"
	teal  = "#2f6f6a"
	amber = "#c68a3e"
	muted = "#cdb99c"
	serif = "Georgia, 'Times New Roman', serif"
	mono  = "ui-monospace, SFMono-Regular, Menlo, monospace"
)

// markShapes writes the 120 x 120 mark. The caller places it in a group.
func markShapes(out *strings.Builder, indent string) {
	fmt.Fprintf(out, "%s<rect width=\"120\" height=\"120\" rx=\"26\" fill=\"%s\"/>\n", indent, ink)
	fmt.Fprintf(out, "%s<rect x=\"18\" y=\"30\" width=\"84\" height=\"60\" rx=\"8\" fill=\"%s\"/>\n", indent, cream)
	fmt.Fprintf(out, "%s<path d=\"M20 36 L60 64 L100 36\" fill=\"none\" stroke=\"%s\" stroke-width=\"4\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>\n", indent, ink)
	fmt.Fprintf(out, "%s<circle cx=\"94\" cy=\"38\" r=\"7\" fill=\"%s\"/>\n", indent, amber)
	fmt.Fprintf(out, "%s<rect x=\"30\" y=\"72\" width=\"58\" height=\"5\" rx=\"2.5\" fill=\"%s\"/>\n", indent, teal)
	fmt.Fprintf(out, "%s<rect x=\"30\" y=\"81\" width=\"40\" height=\"5\" rx=\"2.5\" fill=\"%s\"/>\n", indent, amber)
}

// Mark renders the square product mark, suitable for a favicon or a small
// avatar.
func (s Site) Mark() []byte {
	var out strings.Builder
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	out.WriteString("<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 120 120\" role=\"img\" aria-labelledby=\"mailsense-mark-title mailsense-mark-desc\">\n")
	fmt.Fprintf(&out, "  <title id=\"mailsense-mark-title\">%s</title>\n", html.EscapeString(s.Product))
	out.WriteString("  <desc id=\"mailsense-mark-desc\">An envelope with three ranked lines inside.</desc>\n")
	markShapes(&out, "  ")
	out.WriteString("</svg>\n")
	return []byte(out.String())
}

// Logo renders the horizontal lockup: mark and wordmark. It has no language,
// so the same file serves both trees.
func (s Site) Logo() []byte {
	var out strings.Builder
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	out.WriteString("<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 360 120\" role=\"img\" aria-labelledby=\"mailsense-logo-title mailsense-logo-desc\">\n")
	fmt.Fprintf(&out, "  <title id=\"mailsense-logo-title\">%s</title>\n", html.EscapeString(s.Product))
	out.WriteString("  <desc id=\"mailsense-logo-desc\">The square mark beside the product wordmark.</desc>\n")
	out.WriteString("  <g transform=\"translate(6 6) scale(0.9)\">\n")
	markShapes(&out, "    ")
	out.WriteString("  </g>\n")
	fmt.Fprintf(&out, "  <text x=\"126\" y=\"70\" font-family=\"%s\" font-size=\"46\" letter-spacing=\"-1\" fill=\"%s\">%s</text>\n", serif, ink, html.EscapeString(s.Product))
	fmt.Fprintf(&out, "  <text x=\"128\" y=\"94\" font-family=\"%s\" font-size=\"13\" letter-spacing=\"2.4\" fill=\"%s\">MAIL TRIAGE</text>\n", mono, teal)
	out.WriteString("</svg>\n")
	return []byte(out.String())
}

// Banner renders the social and documentation banner in one language. The
// tagline and the description come from the same localized fact.
func (s Site) Banner(lang string) []byte {
	var out strings.Builder
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	out.WriteString("<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 1200 630\" role=\"img\" aria-labelledby=\"mailsense-banner-title mailsense-banner-desc\">\n")
	fmt.Fprintf(&out, "  <title id=\"mailsense-banner-title\">%s</title>\n", html.EscapeString(s.Product))
	fmt.Fprintf(&out, "  <desc id=\"mailsense-banner-desc\">%s</desc>\n", html.EscapeString(s.Hero.Banner.For(lang)))
	fmt.Fprintf(&out, "  <rect width=\"1200\" height=\"630\" fill=\"%s\"/>\n", cream)
	fmt.Fprintf(&out, "  <rect width=\"1200\" height=\"10\" fill=\"%s\"/>\n", ink)
	out.WriteString("  <g transform=\"translate(80 110) scale(1.5)\">\n")
	markShapes(&out, "    ")
	out.WriteString("  </g>\n")
	fmt.Fprintf(&out, "  <text x=\"300\" y=\"220\" font-family=\"%s\" font-size=\"92\" letter-spacing=\"-2\" fill=\"%s\">%s</text>\n", serif, ink, html.EscapeString(s.Product))
	fmt.Fprintf(&out, "  <text x=\"304\" y=\"286\" font-family=\"%s\" font-size=\"30\" fill=\"%s\">%s</text>\n", serif, teal, html.EscapeString(s.Hero.Banner.For(lang)))
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"322\" width=\"120\" height=\"4\" fill=\"%s\"/>\n", amber)
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"366\" width=\"520\" height=\"18\" rx=\"9\" fill=\"%s\"/>\n", teal)
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"400\" width=\"380\" height=\"18\" rx=\"9\" fill=\"%s\"/>\n", amber)
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"434\" width=\"240\" height=\"18\" rx=\"9\" fill=\"%s\"/>\n", muted)
	repo := strings.TrimPrefix(s.Repository, "https://")
	fmt.Fprintf(&out, "  <text x=\"80\" y=\"580\" font-family=\"%s\" font-size=\"16\" fill=\"%s\">AGPL-3.0-or-later · %s</text>\n", mono, ink, html.EscapeString(repo))
	out.WriteString("</svg>\n")
	return []byte(out.String())
}
