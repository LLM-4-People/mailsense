package ui

import (
	"io/fs"
	"regexp"
	"testing"
)

// staticLabelRE captures an element whose whole content is its fallback label.
var staticLabelRE = regexp.MustCompile(`data-i18n="([^"]+)"[^>]*>([^<]*)<`)

// TestStaticLabelsMatchTheEnglishCatalog: index.html carries the English label
// as the pre-boot fallback, a second copy of the catalog value. The copy must
// equal the catalog exactly; the two had drifted ("Mailbox"/"Today",
// "Hub"/"Home", "Updating"/"Updating…").
func TestStaticLabelsMatchTheEnglishCatalog(t *testing.T) {
	body, err := fs.ReadFile(Files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	en := readCatalog(t, "i18n.en.js")
	checked := 0
	for _, match := range staticLabelRE.FindAllStringSubmatch(page, -1) {
		key, text := match[1], match[2]
		entry, ok := en[key]
		if !ok {
			t.Errorf("index.html references %q, which i18n.en.js lacks", key)
			continue
		}
		if entry.isPlural {
			t.Errorf("index.html uses the plural key %q as static text", key)
			continue
		}
		if text != entry.text {
			t.Errorf("index.html shows %q for %q; the English catalog says %q", text, key, entry.text)
		}
		checked++
	}
	if checked < 20 {
		t.Fatalf("only %d static labels checked; the shell must carry its fallback text", checked)
	}
}
