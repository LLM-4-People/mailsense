// Package site owns the public site's single data source and its renderer.
//
// docs/site.json holds the product facts and both languages. This package
// renders the English and French landing pages, the two READMEs, and the brand
// SVG assets from that one source, so a claim, a feature, or a colour cannot
// drift between them. cmd/gensite writes the files and checks for drift.
package site

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
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
	for index, item := range s.Nav {
		if strings.TrimSpace(item.Key) == "" {
			problems = append(problems, fmt.Sprintf("nav[%d].key is empty", index))
		} else if !pageKeyRE.MatchString(item.Key) {
			problems = append(problems, fmt.Sprintf("nav[%d].key %q is not a lower_snake name", index, item.Key))
		}
		require(fmt.Sprintf("nav[%d].label", index), item.Label)
	}
	problems = append(problems, validatePages(s.Pages())...)
	destinations := s.destinations()
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

// Page is one row of the public page table. The table is the single owner of
// every public destination and every generated output: its order is the
// navigation order, the release page order and the check order.
//
// A page either has a Render function or is an authored public document whose
// English and French files already exist under docs/. The table builder fills
// the file paths from the nav keys in docs/site.json, so adding a page is one
// nav entry plus its two files.
type Page struct {
	Key    string
	Label  Localized
	EN     string
	FR     string
	Nav    bool
	Public bool
	Render func(Site, string) []byte
}

// File returns the repository-relative path of the page in lang.
func (p Page) File(lang string) string {
	if lang == "fr" {
		return p.FR
	}
	return p.EN
}

// DocsName returns the page's file name inside its documentation directory.
func (p Page) DocsName(lang string) string { return path.Base(p.File(lang)) }

// pageKeyRE keeps a nav key safe to turn into a file name. The key is data from
// docs/site.json, so it is validated before it reaches a path.
var pageKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// pageFile is the one key to documentation file convention: underscores in the
// nav key become hyphens in the file name.
func pageFile(key string) string { return strings.ReplaceAll(key, "_", "-") + ".md" }

// Pages returns the one ordered page table. The two READMEs come first so the
// generated output order stays stable, then the landing pages, then the nav
// pages in docs/site.json order, then the brand assets.
func (s Site) Pages() []Page {
	pages := []Page{
		{Key: "readme", EN: "README.md", FR: "README.fr.md",
			Render: func(site Site, lang string) []byte { return site.Readme(lang) }},
		{Key: "index", EN: "docs/index.md", FR: "docs/fr/index.md", Public: true,
			Render: func(site Site, lang string) []byte { return site.Landing(lang) }},
	}
	for _, item := range s.Nav {
		name := pageFile(item.Key)
		pages = append(pages, Page{
			Key: item.Key, Label: item.Label, Nav: true, Public: true,
			EN: "docs/" + name, FR: "docs/fr/" + name,
		})
	}
	pages = append(pages,
		Page{Key: "mark", EN: "docs/assets/mark.svg", FR: "docs/assets/mark.svg",
			Render: func(site Site, _ string) []byte { return site.Mark() }},
		Page{Key: "logo", EN: "docs/assets/logo.svg", FR: "docs/assets/logo.svg",
			Render: func(site Site, _ string) []byte { return site.Logo() }},
		Page{Key: "banner", EN: "docs/assets/banner-en.svg", FR: "docs/assets/banner-fr.svg",
			Render: func(site Site, lang string) []byte { return site.Banner(lang) }},
	)
	return pages
}

// validatePages refuses a table that cannot be rendered or shipped: a duplicate
// key or path, a generated path without a renderer, or a page that is neither
// generated nor an authored public document.
func validatePages(pages []Page) []string {
	var problems []string
	keys := map[string]bool{}
	owners := map[string]string{}
	renderers := map[string]int{}
	for index, page := range pages {
		if strings.TrimSpace(page.Key) == "" {
			problems = append(problems, fmt.Sprintf("page[%d] has no key", index))
			continue
		}
		if !pageKeyRE.MatchString(page.Key) {
			problems = append(problems, fmt.Sprintf("page %q is not a lower_snake name", page.Key))
		}
		if keys[page.Key] {
			problems = append(problems, fmt.Sprintf("page %q is declared twice", page.Key))
		}
		keys[page.Key] = true
		if page.Render == nil && !page.Public {
			problems = append(problems, fmt.Sprintf("page %q has no renderer and is not an authored public page", page.Key))
		}
		for _, file := range []string{page.EN, page.FR} {
			if file == "" {
				problems = append(problems, fmt.Sprintf("page %q has an empty file path", page.Key))
				continue
			}
			if filepath.IsAbs(file) || strings.Contains(file, "..") {
				problems = append(problems, fmt.Sprintf("page %q has an unsafe file path %q", page.Key, file))
			}
			if owner, ok := owners[file]; ok && owner != page.Key {
				problems = append(problems, fmt.Sprintf("pages %q and %q share the file %s", owner, page.Key, file))
				continue
			}
			owners[file] = page.Key
		}
		if page.Render != nil {
			seen := map[string]bool{}
			for _, file := range []string{page.EN, page.FR} {
				if file == "" || seen[file] {
					continue
				}
				seen[file] = true
				renderers[file]++
				if renderers[file] > 1 {
					problems = append(problems, fmt.Sprintf("page %q gives %s a second renderer", page.Key, file))
				}
			}
		}
	}
	return problems
}

// destinations is the nav key set the placeholder validator uses. It comes from
// the same table as the links, so a destination cannot be declared twice.
func (s Site) destinations() map[string]bool {
	out := map[string]bool{}
	for _, page := range s.Pages() {
		if page.Nav {
			out[page.Key] = true
		}
	}
	return out
}

// PublicPages is the documentation set shipped with a release, in reading
// order: the landing page, then every nav page in docs/site.json order.
func (s Site) PublicPages() []string {
	var out []string
	for _, page := range s.Pages() {
		if page.Public {
			out = append(out, page.DocsName("en"))
		}
	}
	return out
}

// PublicPagePaths returns the public pages under docs/, or under docs/fr/ for
// French. It is what the release workflow copies into the tarball.
func (s Site) PublicPagePaths(lang string) []string {
	var out []string
	for _, page := range s.Pages() {
		if page.Public {
			out = append(out, page.File(lang))
		}
	}
	return out
}

// GeneratedPaths is the table's generated output order. A check prints in this
// order, so a diff is stable between runs.
func GeneratedPaths() []string {
	var out []string
	for _, page := range (Site{}).Pages() {
		if page.Render == nil {
			continue
		}
		if page.EN != "" {
			out = append(out, page.EN)
		}
		if page.FR != "" && page.FR != page.EN {
			out = append(out, page.FR)
		}
	}
	return out
}

// Generated renders every output from the one source and the one table.
func (s Site) Generated() (map[string][]byte, error) {
	pages := s.Pages()
	if problems := validatePages(pages); len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	out := make(map[string][]byte)
	for _, page := range pages {
		if page.Render == nil {
			continue
		}
		for _, lang := range []string{"en", "fr"} {
			file := page.File(lang)
			if file == "" {
				continue
			}
			body := page.Render(s, lang)
			if prior, ok := out[file]; ok {
				if !bytes.Equal(prior, body) {
					return nil, fmt.Errorf("page %q renders %s twice with different bytes", page.Key, file)
				}
				continue
			}
			out[file] = body
		}
	}
	return out, nil
}

// validateFiles refuses a missing authored page. A nav key without an English
// or French file is a release defect: the table derives the path, so the file
// is the only part a person has to add.
func (s Site) validateFiles(root string) error {
	var problems []string
	for _, page := range s.Pages() {
		if page.Render != nil || !page.Public {
			continue
		}
		for _, lang := range []string{"en", "fr"} {
			file := page.File(lang)
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(file))); err != nil {
				problems = append(problems, fmt.Sprintf("%s page for %q is missing: %s", lang, page.Key, file))
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// Write renders every output under root and returns the paths it wrote.
func Write(root string) ([]string, error) {
	value, err := Load(root)
	if err != nil {
		return nil, err
	}
	if err := value.validateFiles(root); err != nil {
		return nil, err
	}
	generated, err := value.Generated()
	if err != nil {
		return nil, err
	}
	var written []string
	for _, name := range GeneratedPaths() {
		body, ok := generated[name]
		if !ok {
			return written, fmt.Errorf("no renderer for %s", name)
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return written, fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, body, 0o644); err != nil {
			return written, fmt.Errorf("write %s: %w", name, err)
		}
		written = append(written, name)
	}
	return written, nil
}

// Check compares every output with the source. It returns the stale paths and
// never writes. A missing authored page is an error, not a stale generated file.
func Check(root string) ([]string, error) {
	value, err := Load(root)
	if err != nil {
		return nil, err
	}
	if err := value.validateFiles(root); err != nil {
		return nil, err
	}
	generated, err := value.Generated()
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, name := range GeneratedPaths() {
		body, ok := generated[name]
		if !ok {
			return stale, fmt.Errorf("no renderer for %s", name)
		}
		current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
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
