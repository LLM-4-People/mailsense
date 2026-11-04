// Package site owns the public site's single data source and its renderer.
//
// docs/site.json holds the product facts and both languages. This package
// renders the English and French landing pages, the two READMEs, and the brand
// SVG assets from that one source, so a claim, a feature, or a colour cannot
// drift between them. cmd/gensite writes the files and checks for drift.
package site

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SourcePath is the one data source, relative to the repository root.
const SourcePath = "docs/site.json"

// Localized is one fact in both shipped languages.
type Localized struct {
	EN string `json:"en"`
	FR string `json:"fr"`
}

// For returns the requested language, falling back to English.
func (l Localized) For(lang string) string {
	if lang == "fr" {
		return l.FR
	}
	return l.EN
}

// Feature is one product capability.
type Feature struct {
	Title Localized `json:"title"`
	Body  Localized `json:"body"`
}

// Row is one row of the "what runs where" table.
type Row struct {
	Cells []Localized `json:"cells"`
}

// NavItem is one public destination.
type NavItem struct {
	Key   string    `json:"key"`
	Label Localized `json:"label"`
}

// Site is the complete public surface.
type Site struct {
	Product    string `json:"product"`
	Repository string `json:"repository"`
	Hero       struct {
		Title   Localized `json:"title"`
		Tagline Localized `json:"tagline"`
		Intro   Localized `json:"intro"`
		Banner  Localized `json:"banner"`
	} `json:"hero"`
	Nav      []NavItem `json:"nav"`
	Features struct {
		Title Localized `json:"title"`
		Items []Feature `json:"items"`
	} `json:"features"`
	Difference struct {
		Title Localized `json:"title"`
		Body  Localized `json:"body"`
	} `json:"difference"`
	Runs struct {
		Title   Localized   `json:"title"`
		Columns []Localized `json:"columns"`
		Rows    []Row       `json:"rows"`
		Note    Localized   `json:"note"`
	} `json:"runs"`
	Start struct {
		Title Localized   `json:"title"`
		Steps []Localized `json:"steps"`
	} `json:"start"`
	Status struct {
		Title Localized `json:"title"`
		Body  Localized `json:"body"`
	} `json:"status"`
	Build struct {
		Title        Localized `json:"title"`
		Requirements Localized `json:"requirements"`
		Commands     string    `json:"commands"`
		Note         Localized `json:"note"`
	} `json:"build"`
	License struct {
		Title Localized `json:"title"`
		Body  Localized `json:"body"`
	} `json:"license"`
}

// Load reads and validates the one source.
func Load(root string) (Site, error) {
	body, err := os.ReadFile(filepath.Join(root, SourcePath))
	if err != nil {
		return Site{}, fmt.Errorf("read %s: %w", SourcePath, err)
	}
	var value Site
	if err := json.Unmarshal(body, &value); err != nil {
		return Site{}, fmt.Errorf("parse %s: %w", SourcePath, err)
	}
	if err := value.Validate(); err != nil {
		return Site{}, fmt.Errorf("validate %s: %w", SourcePath, err)
	}
	return value, nil
}

// Validate refuses a source that would render an empty public fact. A missing
// translation is a defect here, not a blank line on the site.
func (s Site) Validate() error {
	var problems []string
	require := func(name string, value Localized) {
		if strings.TrimSpace(value.EN) == "" {
			problems = append(problems, name+": en is empty")
		}
		if strings.TrimSpace(value.FR) == "" {
			problems = append(problems, name+": fr is empty")
		}
	}
	if strings.TrimSpace(s.Product) == "" {
		problems = append(problems, "product is empty")
	}
	if !strings.HasPrefix(s.Repository, "https://") {
		problems = append(problems, "repository must be an https URL")
	}
	require("hero.title", s.Hero.Title)
	require("hero.tagline", s.Hero.Tagline)
	require("hero.intro", s.Hero.Intro)
	require("hero.banner", s.Hero.Banner)
	require("features.title", s.Features.Title)
	if len(s.Features.Items) == 0 {
		problems = append(problems, "features.items is empty")
	}
	for index, feature := range s.Features.Items {
		require(fmt.Sprintf("features.items[%d].title", index), feature.Title)
		require(fmt.Sprintf("features.items[%d].body", index), feature.Body)
	}
	require("difference.title", s.Difference.Title)
	require("difference.body", s.Difference.Body)
	require("runs.title", s.Runs.Title)
	require("runs.note", s.Runs.Note)
	if len(s.Runs.Columns) == 0 {
		problems = append(problems, "runs.columns is empty")
	}
	for index, column := range s.Runs.Columns {
		require(fmt.Sprintf("runs.columns[%d]", index), column)
	}
	for index, row := range s.Runs.Rows {
		if len(row.Cells) != len(s.Runs.Columns) {
			problems = append(problems, fmt.Sprintf("runs.rows[%d] has %d cells, want %d", index, len(row.Cells), len(s.Runs.Columns)))
			continue
		}
		for cell, value := range row.Cells {
			require(fmt.Sprintf("runs.rows[%d].cells[%d]", index, cell), value)
		}
	}
	require("start.title", s.Start.Title)
	if len(s.Start.Steps) == 0 {
		problems = append(problems, "start.steps is empty")
	}
	for index, step := range s.Start.Steps {
		require(fmt.Sprintf("start.steps[%d]", index), step)
	}
	require("status.title", s.Status.Title)
	require("status.body", s.Status.Body)
	require("build.title", s.Build.Title)
	require("build.requirements", s.Build.Requirements)
	require("build.note", s.Build.Note)
	if strings.TrimSpace(s.Build.Commands) == "" {
		problems = append(problems, "build.commands is empty")
	}
	require("license.title", s.License.Title)
	require("license.body", s.License.Body)
	if len(s.Nav) == 0 {
		problems = append(problems, "nav is empty")
	}
	destinations := map[string]bool{
		"install": true, "first_run": true, "mailboxes": true, "providers": true,
		"learning": true, "faq": true, "privacy": true, "security": true,
		"troubleshooting": true, "contributing": true,
	}
	for index, item := range s.Nav {
		if strings.TrimSpace(item.Key) == "" {
			problems = append(problems, fmt.Sprintf("nav[%d].key is empty", index))
		} else if !destinations[item.Key] {
			problems = append(problems, fmt.Sprintf("nav[%d].key %q has no destination", index, item.Key))
		}
		require(fmt.Sprintf("nav[%d].label", index), item.Label)
	}
	placeholders := regexp.MustCompile(`\{([a-z_]+)\}`)
	checkPlaceholders := func(name, text string) {
		for _, match := range placeholders.FindAllStringSubmatch(text, -1) {
			if !destinations[match[1]] {
				problems = append(problems, fmt.Sprintf("%s: placeholder {%s} has no destination", name, match[1]))
			}
		}
	}
	checkPlaceholders("runs.note.en", s.Runs.Note.EN)
	checkPlaceholders("runs.note.fr", s.Runs.Note.FR)
	for index, step := range s.Start.Steps {
		checkPlaceholders(fmt.Sprintf("start.steps[%d].en", index), step.EN)
		checkPlaceholders(fmt.Sprintf("start.steps[%d].fr", index), step.FR)
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// PublicPages is the documentation set shipped with a release, in reading
// order. A page is declared once here; the release packaging asks the
// generator for this list instead of keeping a second copy.
var PublicPages = []string{
	"index.md",
	"install.md",
	"first-run.md",
	"mailboxes.md",
	"providers.md",
	"learning.md",
	"faq.md",
	"privacy.md",
	"security.md",
	"troubleshooting.md",
	"contributing.md",
}

// PublicPagePaths returns the public pages under docs/, or under docs/fr/ for
// French. It is what the release workflow copies into the tarball.
func PublicPagePaths(lang string) []string {
	dir := "docs/"
	if lang == "fr" {
		dir = "docs/fr/"
	}
	out := make([]string, 0, len(PublicPages))
	for _, name := range PublicPages {
		out = append(out, dir+name)
	}
	return out
}

// GeneratedPaths is the fixed output order. A check prints in this order, so a
// diff is stable between runs.
func GeneratedPaths() []string {
	return []string{
		"README.md",
		"README.fr.md",
		"docs/index.md",
		"docs/fr/index.md",
		"docs/assets/mark.svg",
		"docs/assets/logo.svg",
		"docs/assets/banner-en.svg",
		"docs/assets/banner-fr.svg",
	}
}

// Generated renders every output from the one source.
func (s Site) Generated() map[string][]byte {
	return map[string][]byte{
		"README.md":                 s.Readme("en"),
		"README.fr.md":              s.Readme("fr"),
		"docs/index.md":             s.Landing("en"),
		"docs/fr/index.md":          s.Landing("fr"),
		"docs/assets/mark.svg":      s.Mark(),
		"docs/assets/logo.svg":      s.Logo(),
		"docs/assets/banner-en.svg": s.Banner("en"),
		"docs/assets/banner-fr.svg": s.Banner("fr"),
	}
}

// Write renders every output under root and returns the paths it wrote.
func Write(root string) ([]string, error) {
	value, err := Load(root)
	if err != nil {
		return nil, err
	}
	generated := value.Generated()
	var written []string
	for _, name := range GeneratedPaths() {
		body, ok := generated[name]
		if !ok {
			return written, fmt.Errorf("no renderer for %s", name)
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return written, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return written, fmt.Errorf("write %s: %w", name, err)
		}
		written = append(written, name)
	}
	return written, nil
}

// Check compares every output with the source. It returns the stale paths and
// never writes.
func Check(root string) ([]string, error) {
	value, err := Load(root)
	if err != nil {
		return nil, err
	}
	generated := value.Generated()
	var stale []string
	for _, name := range GeneratedPaths() {
		body, ok := generated[name]
		if !ok {
			return stale, fmt.Errorf("no renderer for %s", name)
		}
		current, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			stale = append(stale, name)
			continue
		}
		if string(current) != string(body) {
			stale = append(stale, name)
		}
	}
	return stale, nil
}
