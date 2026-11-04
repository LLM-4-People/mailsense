package site

import (
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pngSize reads only the PNG header. A capture that was resized by hand or a
// social card exported at the wrong size fails here before it ships.
func pngSize(t *testing.T, name string) (int, int) {
	t.Helper()
	file, err := os.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer file.Close()
	config, err := png.DecodeConfig(file)
	if err != nil {
		t.Fatalf("%s is not a readable PNG: %v", name, err)
	}
	return config.Width, config.Height
}

// TestScreenshotAssetsMatchTheMediaTable is the asset truth check: every
// screen in the one media table has a capture in both languages at the size
// the pages declare, and the social preview matches its own entry.
func TestScreenshotAssetsMatchTheMediaTable(t *testing.T) {
	root := root(t)
	value, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Media.Screenshots) == 0 {
		t.Fatal("the media table lists no screenshots")
	}
	for _, shot := range value.Media.Screenshots {
		for _, lang := range []string{"en", "fr"} {
			name := filepath.Join(root, filepath.FromSlash(value.ScreenshotPath(shot.Key, lang)))
			width, height := pngSize(t, name)
			if width != value.Media.ScreenshotWidth || height != value.Media.ScreenshotHeight {
				t.Errorf("%s is %dx%d, want %dx%d", name, width, height,
					value.Media.ScreenshotWidth, value.Media.ScreenshotHeight)
			}
		}
	}
	social := filepath.Join(root, filepath.FromSlash(value.Media.SocialPreview))
	width, height := pngSize(t, social)
	if width != value.Media.SocialWidth || height != value.Media.SocialHeight {
		t.Errorf("%s is %dx%d, want %dx%d", social, width, height,
			value.Media.SocialWidth, value.Media.SocialHeight)
	}
}

// TestLandingReferencesOnlyExistingAssets walks every image the generated
// landing pages name and resolves it the way Jekyll will. A renamed capture or
// a typo in the table cannot ship a broken image.
func TestLandingReferencesOnlyExistingAssets(t *testing.T) {
	root := root(t)
	value, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	imageTag := regexp.MustCompile(`<img[^>]+src="([^"]+)"`)
	for _, lang := range []string{"en", "fr"} {
		base := "docs"
		if lang == "fr" {
			base = "docs/fr"
		}
		landing := string(value.Landing(lang))
		sources := imageTag.FindAllStringSubmatch(landing, -1)
		if len(sources) < 2 {
			t.Errorf("%s landing names only %d images, want the hero and the captures", lang, len(sources))
		}
		seen := map[string]bool{}
		for _, match := range sources {
			src := match[1]
			if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
				t.Errorf("%s landing loads a remote image: %s", lang, src)
				continue
			}
			if seen[src] {
				continue
			}
			seen[src] = true
			target := filepath.Join(root, filepath.FromSlash(path.Join(base, src)))
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s landing references a missing asset %s", lang, src)
			}
		}
	}
	// The two READMEs carry the generated banner. Resolve each Markdown image
	// from the repository root, where a reader of the file will look.
	markdownImage := regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)
	for _, name := range []string{"README.md", "README.fr.md"} {
		for _, match := range markdownImage.FindAllStringSubmatch(readDoc(t, root, name), -1) {
			src := match[1]
			if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
				t.Errorf("%s loads a remote image: %s", name, src)
				continue
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(src))); err != nil {
				t.Errorf("%s references a missing asset %s", name, src)
			}
		}
	}
}

// TestLandingFrontMatterDrivesTheSocialCard checks the one mechanism the
// bundled minima theme gives us: its head calls jekyll-seo-tag, so the
// generated front matter has to name the committed PNG with its real size.
// The theme emits og:image, og:image:width, og:image:height, twitter:card
// summary_large_image and twitter:image from exactly these fields.
func TestLandingFrontMatterDrivesTheSocialCard(t *testing.T) {
	root := root(t)
	value, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	image := "/" + strings.TrimPrefix(value.Media.SocialPreview, "docs/")
	for _, lang := range []string{"en", "fr"} {
		landing := string(value.Landing(lang))
		for _, want := range []string{
			"image:\n",
			"  path: \"" + image + "\"\n",
			fmt.Sprintf("  width: %d\n", value.Media.SocialWidth),
			fmt.Sprintf("  height: %d\n", value.Media.SocialHeight),
			"  alt: ",
		} {
			if !strings.Contains(landing, want) {
				t.Errorf("%s landing front matter is missing %q", lang, want)
			}
		}
		target := filepath.Join(root, filepath.FromSlash(path.Join("docs", strings.TrimPrefix(image, "/"))))
		if _, err := os.Stat(target); err != nil {
			t.Errorf("%s social card names a missing image: %s", lang, image)
		}
	}
	config := string(value.Config())
	if !strings.Contains(config, "url: \""+value.siteOrigin()+"\"") {
		t.Error("the generated config has no absolute site origin for card URLs")
	}
	if !strings.Contains(config, "theme: \"minima\"") {
		t.Error("the generated config no longer uses the theme whose head emits the cards")
	}
}

// senderEntry is one invented sender the capture script may show.
type senderEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// TestCaptureScriptNamesOnlyInventedSenders reads the declared cast and scans
// the capture sources for any address outside it. A real mailbox, a personal
// name or a live provider domain cannot slip into a screenshot fixture.
func TestCaptureScriptNamesOnlyInventedSenders(t *testing.T) {
	root := root(t)
	dir := filepath.Join(root, "e2e", "screenshots")
	body, err := os.ReadFile(filepath.Join(dir, "senders.json"))
	if err != nil {
		t.Fatalf("read the declared sender cast: %v", err)
	}
	var senders []senderEntry
	if err := json.Unmarshal(body, &senders); err != nil {
		t.Fatalf("parse the declared sender cast: %v", err)
	}
	if len(senders) == 0 {
		t.Fatal("the declared sender cast is empty")
	}
	declared := map[string]bool{}
	addresses := map[string]bool{}
	freeMail := regexp.MustCompile(`(?i)@(gmail|googlemail|outlook|hotmail|live|yahoo|proton|icloud|aol)\.`)
	for _, sender := range senders {
		if strings.TrimSpace(sender.Name) == "" {
			t.Error("a declared sender has no name")
		}
		if !strings.HasSuffix(sender.Address, ".test") {
			t.Errorf("sender %q uses %q, which is not a reserved .test address", sender.Name, sender.Address)
		}
		if freeMail.MatchString(sender.Address) {
			t.Errorf("sender %q uses a live mailbox provider: %s", sender.Name, sender.Address)
		}
		if addresses[sender.Address] {
			t.Errorf("sender address %q is declared twice", sender.Address)
		}
		addresses[sender.Address] = true
		declared[sender.Name] = true
		declared[sender.Address] = true
	}

	email := regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the capture directory: %v", err)
	}
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !(strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".mjs") || strings.HasSuffix(name, ".json")) {
			continue
		}
		scanned++
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range email.FindAllString(string(content), -1) {
			if !declared[match] {
				t.Errorf("%s names %q, which is not in the declared invented cast", name, match)
			}
		}
	}
	if scanned < 2 {
		t.Fatal("the capture directory has too few files to scan")
	}

	// The fixtures must read the declared cast, and any display name they
	// spell must come from it.
	fixture, err := os.ReadFile(filepath.Join(dir, "data.ts"))
	if err != nil {
		t.Fatalf("read the capture fixtures: %v", err)
	}
	if !strings.Contains(string(fixture), "senders.json") {
		t.Error("the capture fixtures do not read the declared sender cast")
	}
	spelled := regexp.MustCompile(`(?:sender|sender_name):\s*"([^"]+)"`)
	for _, match := range spelled.FindAllStringSubmatch(string(fixture), -1) {
		if !declared[match[1]] {
			t.Errorf("the capture fixtures name %q, which is not in the declared invented cast", match[1])
		}
	}
}

// TestEveryMediaScreenHasACaptureRoutine keeps the one media table and the
// browser script in step: adding a screen without a capture is a failing test,
// not a silently missing image.
func TestEveryMediaScreenHasACaptureRoutine(t *testing.T) {
	root := root(t)
	value, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(root, "e2e", "screenshots", "capture.spec.ts"))
	if err != nil {
		t.Fatalf("read the capture script: %v", err)
	}
	text := string(script)
	if !strings.Contains(text, "media.screenshots") {
		t.Error("the capture script does not read the screen list from docs/site.json")
	}
	routine := regexp.MustCompile(`(?m)^\s*([a-z][a-z0-9_]*):\s*async\s*\(page`)
	found := map[string]bool{}
	for _, match := range routine.FindAllStringSubmatch(text, -1) {
		found[match[1]] = true
	}
	for _, shot := range value.Media.Screenshots {
		if !found[shot.Key] {
			t.Errorf("the capture script has no routine for %q", shot.Key)
		}
	}
}
