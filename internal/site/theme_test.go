package site

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// cssBlock returns the body of the first block whose selector line is
// "selector {". It counts braces, so the indented blocks inside a media query
// parse like the top-level ones.
func cssBlock(t *testing.T, css, selector string) string {
	t.Helper()
	marker := selector + " {"
	start := strings.Index(css, marker)
	if start < 0 {
		return ""
	}
	start += len(marker)
	depth := 1
	for index := start; index < len(css); index++ {
		switch css[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return css[start:index]
			}
		}
	}
	return ""
}

var cssTokenRE = regexp.MustCompile(`--([a-z0-9-]+):\s*([^;]+);`)

// cssTokens parses the custom properties of one generated block.
func cssTokens(t *testing.T, block string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, match := range cssTokenRE.FindAllStringSubmatch(block, -1) {
		out[match[1]] = strings.TrimSpace(match[2])
	}
	return out
}

// TestHeadScriptOwnsTheStoredTheme proves the theme choice has one named
// storage key, is applied to the document before the language logic can
// return, and drives the pressed state of the control.
func TestHeadScriptOwnsTheStoredTheme(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	head := string(value.Head())
	if got := strings.Count(head, "<script"); got != 1 {
		t.Fatalf("the head has %d script tags, want one inline script", got)
	}
	// The script is not gated on the landing flag: the theme is applied on
	// every page, while only the alternates belong to the landing pages.
	if !strings.Contains(head, "{%- endif -%}\n  <script>") {
		t.Error("the script is still inside the landing-only condition")
	}
	script := scriptBetween(t, head)
	if !strings.Contains(script, fmt.Sprintf("var THEME = %q;", ThemeStorageKey)) {
		t.Error("the script does not initialise THEME from ThemeStorageKey")
	}
	if got := strings.Count(script, fmt.Sprintf("%q", ThemeStorageKey)); got != 1 {
		t.Errorf("the script names the theme key %d times, want exactly one", got)
	}
	if got := strings.Count(script, "localStorage.getItem(THEME)"); got != 1 {
		t.Errorf("the stored theme has %d readers, want one", got)
	}
	if got := strings.Count(script, "localStorage.setItem(THEME, value)"); got != 1 {
		t.Errorf("the stored theme has %d writers, want one", got)
	}
	if got := strings.Count(script, "localStorage.removeItem(THEME)"); got != 1 {
		t.Errorf("the system choice has %d removers, want one", got)
	}
	for _, want := range []string{
		"document.documentElement.dataset.theme",
		`document.querySelectorAll("[data-theme-choice]")`,
		`button.setAttribute("aria-pressed"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the script is missing %q", want)
		}
	}
	if apply, here := strings.Index(script, "applyTheme(storedTheme())"), strings.Index(script, "var here = "); apply < 0 || here < 0 || apply > here {
		t.Error("the script does not apply the stored theme before the language logic")
	}
}

// TestStylesheetShipsBothPalettes proves the generated stylesheet carries the
// light table, the automatic dark mapping behind the system media query and
// the explicit dark mapping, with identical values, all from brand.go.
func TestStylesheetShipsBothPalettes(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	css := string(value.Stylesheet())
	if !strings.Contains(css, "@media (prefers-color-scheme: dark)") {
		t.Fatal("the stylesheet has no automatic dark mapping")
	}
	if !strings.Contains(css, `:root[data-theme="dark"]`) {
		t.Fatal("the stylesheet has no explicit dark mapping")
	}
	light := cssTokens(t, cssBlock(t, css, ":root"))
	auto := cssTokens(t, cssBlock(t, css, `:root:not([data-theme="light"])`))
	explicit := cssTokens(t, cssBlock(t, css, `:root[data-theme="dark"]`))
	if len(light) == 0 || len(auto) == 0 || len(explicit) == 0 {
		t.Fatalf("palette blocks: light=%d auto=%d explicit=%d", len(light), len(auto), len(explicit))
	}
	if len(auto) != len(explicit) {
		t.Fatalf("the automatic dark mapping has %d tokens and the explicit one %d", len(auto), len(explicit))
	}
	for name, value := range auto {
		if explicit[name] != value {
			t.Errorf("--%s is %q in the automatic mapping and %q in the explicit one", name, value, explicit[name])
		}
	}
	dark := map[string]string{}
	for name, value := range light {
		dark[name] = value
	}
	for name, value := range explicit {
		dark[name] = value
	}
	for _, token := range themeTokens {
		if light[token.Token] != token.Light {
			t.Errorf("light --%s = %q, want %q", token.Token, light[token.Token], token.Light)
		}
		if dark[token.Token] != token.Dark {
			t.Errorf("dark --%s = %q, want %q", token.Token, dark[token.Token], token.Dark)
		}
	}
	// Print forces the light palette back, whatever the screen shows.
	print := cssBlock(t, css, "@media print")
	if print == "" {
		t.Fatal("the stylesheet has no print block")
	}
	for _, name := range []string{"page", "page-ink", "code-block", "code-block-ink"} {
		if !strings.Contains(print, "--"+name+": "+light[name]+";") {
			t.Errorf("the print block does not reset --%s to the light value", name)
		}
	}
}

// TestHeaderCarriesTheThemeControl proves the generated masthead offers the
// three localized choices in both the bar and the menu, pressed on the system
// default, next to the language switch.
func TestHeaderCarriesTheThemeControl(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	header := string(value.Header())
	for _, choice := range value.themeChoices() {
		button := fmt.Sprintf(`data-theme-choice="%s"`, choice.Value)
		if got := strings.Count(header, button); got != 4 {
			t.Errorf("%s appears %d times, want the bar and the menu for two languages", button, got)
		}
		for _, label := range []string{choice.Label.EN, choice.Label.FR} {
			if !strings.Contains(header, label) {
				t.Errorf("the theme control does not carry the %q label", label)
			}
		}
	}
	if got := strings.Count(header, `<button type="button" class="masthead-theme-button"`); got != 12 {
		t.Errorf("the header has %d theme buttons, want twelve", got)
	}
	if got := strings.Count(header, `aria-pressed="true"`); got != 4 {
		t.Errorf("the header has %d pressed buttons, want the four system defaults", got)
	}
	if got := strings.Count(header, `aria-pressed="false"`); got != 8 {
		t.Errorf("the header has %d unpressed buttons, want eight", got)
	}
	if got := strings.Count(header, `role="group"`); got != 4 {
		t.Errorf("the header has %d theme groups, want four", got)
	}
	for _, label := range []string{value.Chrome.Theme.EN, value.Chrome.Theme.FR} {
		if !strings.Contains(header, label) {
			t.Errorf("the theme group has no %q aria-label", label)
		}
	}
	if switchAt, themeAt := strings.Index(header, "data-lang-switch"), strings.Index(header, "data-theme-choice"); switchAt < 0 || themeAt < 0 || switchAt > themeAt {
		t.Error("the theme control does not follow the language switch")
	}
}

// color is one parsed CSS colour. Channels are 0 to 1; alpha defaults to 1.
type color struct{ r, g, b, a float64 }

var (
	hexColorRE  = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	rgbaColorRE = regexp.MustCompile(`^rgba?\(([^)]*)\)$`)
)

// parseColor reads the hex and rgb/rgba forms the palette may emit.
func parseColor(t *testing.T, value string) color {
	t.Helper()
	if match := hexColorRE.FindStringSubmatch(value); match != nil {
		hex := match[1]
		if len(hex) == 3 {
			hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
		}
		component := func(part string) float64 {
			number, err := strconv.ParseUint(part, 16, 8)
			if err != nil {
				t.Fatalf("cannot parse colour %q: %v", value, err)
			}
			return float64(number) / 255
		}
		return color{component(hex[0:2]), component(hex[2:4]), component(hex[4:6]), 1}
	}
	if match := rgbaColorRE.FindStringSubmatch(value); match != nil {
		parts := strings.Split(match[1], ",")
		if len(parts) != 3 && len(parts) != 4 {
			t.Fatalf("cannot parse colour %q: %d channels", value, len(parts))
		}
		var parsed [4]float64
		for index, part := range parts {
			number, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil {
				t.Fatalf("cannot parse colour %q: %v", value, err)
			}
			if index < 3 {
				parsed[index] = number / 255
			} else {
				parsed[index] = number
			}
		}
		if len(parts) == 3 {
			parsed[3] = 1
		}
		return color{parsed[0], parsed[1], parsed[2], parsed[3]}
	}
	t.Fatalf("cannot parse colour %q", value)
	return color{}
}

// luminance is the WCAG 2 relative luminance of one opaque colour.
func luminance(c color) float64 {
	channel := func(value float64) float64 {
		if value <= 0.03928 {
			return value / 12.92
		}
		return math.Pow((value+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(c.r) + 0.7152*channel(c.g) + 0.0722*channel(c.b)
}

// contrastRatio is the WCAG 2 contrast ratio. A translucent foreground is
// composited over its background first.
func contrastRatio(t *testing.T, foreground, background string) float64 {
	t.Helper()
	fg := parseColor(t, foreground)
	bg := parseColor(t, background)
	if fg.a < 1 {
		fg = color{
			r: fg.r*fg.a + bg.r*(1-fg.a),
			g: fg.g*fg.a + bg.g*(1-fg.a),
			b: fg.b*fg.a + bg.b*(1-fg.a),
			a: 1,
		}
	}
	first, second := luminance(fg), luminance(bg)
	high, low := math.Max(first, second), math.Min(first, second)
	return (high + 0.05) / (low + 0.05)
}

// contrastPair is one foreground and background the design relies on. Body
// and small text tokens hold the WCAG AA text threshold; the accent and
// pressed-control pairs hold the large text and UI boundary threshold.
type contrastPair struct {
	name    string
	ink     string
	surface string
	minimum float64
}

// contrastPairs is the one contrast contract for both palettes.
var contrastPairs = []contrastPair{
	{"body text", "page-ink", "page", 4.5},
	{"card text", "page-ink", "card", 4.5},
	{"body muted", "muted", "page", 4.5},
	{"card muted", "muted", "card", 4.5},
	{"page link", "teal", "page", 4.5},
	{"card link", "teal", "card", 4.5},
	{"page label", "amber-ink", "page", 4.5},
	{"card label", "amber-ink", "card", 4.5},
	{"panel text", "panel-ink", "panel", 4.5},
	{"panel muted", "panel-muted", "panel", 4.5},
	{"panel link", "panel-quiet", "panel", 4.5},
	{"chip text", "chip-ink", "chip", 4.5},
	{"inline code", "code-ink", "code", 4.5},
	{"code block", "code-block-ink", "code-block", 4.5},
	{"install file", "panel-code-ink", "panel-code", 4.5},
	{"guarantee note", "teal", "wash", 4.5},
	{"button text", "accent-ink", "mint", 4.5},
	{"selected chip", "teal-ink", "teal", 4.5},
	{"panel mint accent", "mint", "panel", 4.5},
	{"panel amber accent", "amber", "panel", 4.5},
	{"focus and hover boundary", "teal", "card", 3.0},
	{"pressed control boundary", "panel-ink", "panel", 3.0},
}

// TestBothPalettesMeetTheContrastContract parses the generated stylesheet
// back into both palettes and checks every declared pair against its WCAG AA
// threshold, so a tuned value cannot quietly drop below it.
func TestBothPalettesMeetTheContrastContract(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	css := string(value.Stylesheet())
	light := cssTokens(t, cssBlock(t, css, ":root"))
	explicit := cssTokens(t, cssBlock(t, css, `:root[data-theme="dark"]`))
	palettes := map[string]map[string]string{"light": {}, "dark": {}}
	for name, value := range light {
		palettes["light"][name] = value
		palettes["dark"][name] = value
	}
	for name, value := range explicit {
		palettes["dark"][name] = value
	}
	for _, theme := range []string{"light", "dark"} {
		tokens := palettes[theme]
		for _, pair := range contrastPairs {
			foreground, ok := tokens[pair.ink]
			if !ok {
				t.Fatalf("%s palette has no --%s", theme, pair.ink)
			}
			background, ok := tokens[pair.surface]
			if !ok {
				t.Fatalf("%s palette has no --%s", theme, pair.surface)
			}
			got := contrastRatio(t, foreground, background)
			t.Logf("%s: %-26s %5.2f:1 (min %.1f)", theme, pair.name, got, pair.minimum)
			if got < pair.minimum {
				t.Errorf("%s: %s on %s = %.2f:1, want at least %.1f:1", theme, pair.name, pair.surface, got, pair.minimum)
			}
		}
	}
}

// TestContrastCheckBites proves the checker rejects a bad pair instead of only
// accepting the current palette.
func TestContrastCheckBites(t *testing.T) {
	if got := contrastRatio(t, "#777777", "#777777"); math.Abs(got-1) > 0.001 {
		t.Fatalf("identical colours = %.3f, want 1", got)
	}
	if got := contrastRatio(t, "#9c9c9c", "#f4efe5"); got >= 4.5 {
		t.Fatalf("light grey on paper = %.2f, want below 4.5", got)
	}
	if got := contrastRatio(t, "#9db3ae", "#0b191a"); got < 4.5 {
		t.Fatalf("mist on ink = %.2f, want at least 4.5", got)
	}
	// The rgba form composites: a paper hairline at 16% over ink is a low
	// contrast boundary, not body text.
	if got := contrastRatio(t, "rgba(244, 239, 229, 0.16)", "#0b191a"); got < 1 || got >= 4.5 {
		t.Fatalf("panel rule over ink = %.2f, want a low boundary value", got)
	}
}
