package site

import (
	"fmt"
	"html"
	"strings"
)

// The brand palette. One declaration feeds every generated asset and the
// stylesheet, so a colour cannot drift between the mark and the page.
const (
	ink   = "#141b1f"
	paper = "#f7f3ea"
	card  = "#fffdf8"
	teal  = "#1d5c58"
	amber = "#a9641f"
	sand  = "#e6d9c2"
	muted = "#5c6a6d"
	rule  = "#d8cdb9"

	serif = "Georgia, 'Times New Roman', serif"
	sans  = "'Segoe UI', -apple-system, BlinkMacSystemFont, Roboto, 'Helvetica Neue', Arial, sans-serif"
	mono  = "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
)

// markShapes writes the 120 x 120 mark. The caller places it in a group.
func markShapes(out *strings.Builder, indent string) {
	fmt.Fprintf(out, "%s<rect width=\"120\" height=\"120\" rx=\"28\" fill=\"%s\"/>\n", indent, ink)
	fmt.Fprintf(out, "%s<rect x=\"18\" y=\"34\" width=\"84\" height=\"56\" rx=\"9\" fill=\"%s\"/>\n", indent, paper)
	fmt.Fprintf(out, "%s<path d=\"M21 40 L60 66 L99 40\" fill=\"none\" stroke=\"%s\" stroke-width=\"4\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>\n", indent, ink)
	fmt.Fprintf(out, "%s<circle cx=\"95\" cy=\"37\" r=\"7\" fill=\"%s\"/>\n", indent, amber)
	fmt.Fprintf(out, "%s<rect x=\"29\" y=\"70\" width=\"54\" height=\"5\" rx=\"2.5\" fill=\"%s\"/>\n", indent, teal)
	fmt.Fprintf(out, "%s<rect x=\"29\" y=\"79\" width=\"39\" height=\"5\" rx=\"2.5\" fill=\"%s\"/>\n", indent, amber)
	fmt.Fprintf(out, "%s<rect x=\"29\" y=\"88\" width=\"24\" height=\"5\" rx=\"2.5\" fill=\"%s\"/>\n", indent, muted)
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
// so the same file serves both trees.
func (s Site) Logo() []byte {
	var out strings.Builder
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	out.WriteString("<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 400 120\" role=\"img\" aria-labelledby=\"mailsense-logo-title mailsense-logo-desc\">\n")
	fmt.Fprintf(&out, "  <title id=\"mailsense-logo-title\">%s</title>\n", html.EscapeString(s.Product))
	out.WriteString("  <desc id=\"mailsense-logo-desc\">The square mark beside the Mailsense wordmark.</desc>\n")
	out.WriteString("  <g transform=\"translate(4 4) scale(0.92)\">\n")
	markShapes(&out, "    ")
	out.WriteString("  </g>\n")
	fmt.Fprintf(&out, "  <text x=\"132\" y=\"66\" font-family=\"%s\" font-size=\"46\" letter-spacing=\"-1\" fill=\"%s\">%s</text>\n", serif, ink, html.EscapeString(s.Product))
	fmt.Fprintf(&out, "  <text x=\"134\" y=\"92\" font-family=\"%s\" font-size=\"12\" letter-spacing=\"2.6\" fill=\"%s\">MAIL TRIAGE</text>\n", mono, teal)
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
	fmt.Fprintf(&out, "  <rect width=\"1200\" height=\"630\" fill=\"%s\"/>\n", paper)
	fmt.Fprintf(&out, "  <rect width=\"1200\" height=\"12\" fill=\"%s\"/>\n", ink)
	out.WriteString("  <g transform=\"translate(72 86) scale(1.45)\">\n")
	markShapes(&out, "    ")
	out.WriteString("  </g>\n")
	fmt.Fprintf(&out, "  <text x=\"300\" y=\"168\" font-family=\"%s\" font-size=\"17\" letter-spacing=\"3.2\" fill=\"%s\">%s</text>\n", mono, teal, html.EscapeString(strings.ToUpper(s.Hero.Eyebrow.For(lang))))
	fmt.Fprintf(&out, "  <text x=\"300\" y=\"248\" font-family=\"%s\" font-size=\"88\" letter-spacing=\"-2\" fill=\"%s\">%s</text>\n", serif, ink, html.EscapeString(s.Product))
	fmt.Fprintf(&out, "  <text x=\"304\" y=\"318\" font-family=\"%s\" font-size=\"34\" fill=\"%s\">%s</text>\n", serif, teal, html.EscapeString(first))
	if second != "" {
		fmt.Fprintf(&out, "  <text x=\"304\" y=\"360\" font-family=\"%s\" font-size=\"34\" fill=\"%s\">%s</text>\n", serif, teal, html.EscapeString(second))
	}
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"392\" width=\"120\" height=\"4\" fill=\"%s\"/>\n", amber)
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"424\" width=\"520\" height=\"18\" rx=\"9\" fill=\"%s\"/>\n", teal)
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"456\" width=\"380\" height=\"18\" rx=\"9\" fill=\"%s\"/>\n", amber)
	fmt.Fprintf(&out, "  <rect x=\"304\" y=\"488\" width=\"240\" height=\"18\" rx=\"9\" fill=\"%s\"/>\n", muted)
	repo := strings.TrimPrefix(s.Repository, "https://")
	fmt.Fprintf(&out, "  <text x=\"72\" y=\"580\" font-family=\"%s\" font-size=\"16\" fill=\"%s\">%s · %s</text>\n", mono, ink, LicenseID, html.EscapeString(repo))
	out.WriteString("</svg>\n")
	return []byte(out.String())
}
