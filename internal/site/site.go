// Package site owns the public site's single data source and its renderer.
//
// docs/site.json holds the product facts and both languages. This package
// renders the English and French landing pages, the two READMEs, the brand SVG
// assets, the stylesheet and the Jekyll configuration from that one source, so
// a claim, a feature, a colour or a page list cannot drift between them.
// cmd/gensite writes the files and checks for drift.
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
	"sort"
	"strings"
)

// SourcePath is the one data source, relative to the repository root.
const SourcePath = "docs/site.json"

// CatalogPath is the one owner of the local runtime and model sizes.
const CatalogPath = "internal/sidecar/runtimes.json"

// LicenseID is the one public license identifier. The truth test ties it to
// LICENSE, so a generated page cannot claim a license the repository lacks.
const LicenseID = "AGPL-3.0-or-later"

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

// Difference is one of the three mechanisms the product claims.
type Difference struct {
	Tag   Localized `json:"tag"`
	Title Localized `json:"title"`
	Body  Localized `json:"body"`
}

// Limit is one honest current limitation.
type Limit struct {
	Title Localized `json:"title"`
	Body  Localized `json:"body"`
}

// NavItem is one public destination.
type NavItem struct {
	Key     string    `json:"key"`
	Label   Localized `json:"label"`
	Primary bool      `json:"primary"`
}

// Facts are the public numbers derived from their owners, never typed twice.
type Facts struct {
	RuntimeWindowsMB int
	RuntimeLinuxMB   int
}

// Site is the complete public surface.
type Site struct {
	Product    string `json:"product"`
	Repository string `json:"repository"`
	DocsSite   string `json:"docs_site"`
	BaseURL    string `json:"baseurl"`
	Hero       struct {
		Eyebrow       Localized `json:"eyebrow"`
		Title         Localized `json:"title"`
		Promise       Localized `json:"promise"`
		Lede          Localized `json:"lede"`
		Banner        Localized `json:"banner"`
		Note          Localized `json:"note"`
		FigureAlt     Localized `json:"figure_alt"`
		FigureCaption Localized `json:"figure_caption"`
	} `json:"hero"`
	Nav         []NavItem `json:"nav"`
	Differences struct {
		Title Localized    `json:"title"`
		Lead  Localized    `json:"lead"`
		Items []Difference `json:"items"`
	} `json:"differences"`
	Boundary struct {
		Title       Localized   `json:"title"`
		Lead        Localized   `json:"lead"`
		LeavesTitle Localized   `json:"leaves_title"`
		Leaves      []Localized `json:"leaves"`
		StaysTitle  Localized   `json:"stays_title"`
		Stays       []Localized `json:"stays"`
		Note        Localized   `json:"note"`
	} `json:"boundary"`
	Limits struct {
		Title Localized `json:"title"`
		Lead  Localized `json:"lead"`
		Items []Limit   `json:"items"`
	} `json:"limits"`
	Install struct {
		Title       Localized `json:"title"`
		Lead        Localized `json:"lead"`
		Windows     Localized `json:"windows"`
		Linux       Localized `json:"linux"`
		Command     string    `json:"command"`
		CommandNote Localized `json:"command_note"`
		FirstRun    Localized `json:"first_run"`
		Note        Localized `json:"note"`
	} `json:"install"`
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

	Facts    Facts
	excluded []string
}

// Load reads and validates the one source, then derives the public facts and
// the Jekyll exclude list from the repository.
func Load(root string) (Site, error) {
	body, err := os.ReadFile(filepath.Join(root, SourcePath))
	if err != nil {
		return Site{}, fmt.Errorf("read %s: %w", SourcePath, err)
	}
	var value Site
	if err := json.Unmarshal(body, &value); err != nil {
		return Site{}, fmt.Errorf("parse %s: %w", SourcePath, err)
	}
	if value.Facts, err = deriveFacts(root); err != nil {
		return Site{}, err
	}
	value.excluded = excludedDocs(root, value)
	if err := value.Validate(); err != nil {
		return Site{}, fmt.Errorf("validate %s: %w", SourcePath, err)
	}
	return value, nil
}

// runtimeCatalog is the part of the pinned catalog the public sizes need.
type runtimeCatalog struct {
	Runtimes map[string]struct {
		Platforms map[string]struct {
			Variants map[string]struct {
				Artifacts []struct {
					Size int64 `json:"size"`
				} `json:"artifacts"`
			} `json:"variants"`
		} `json:"platforms"`
	} `json:"runtimes"`
	Models map[string]struct {
		Size int64 `json:"size"`
	} `json:"models"`
	LocalEmbedder struct {
		Runtime string `json:"runtime"`
		Model   string `json:"model"`
	} `json:"local_embedder"`
}

// deriveFacts reads the pinned runtime catalog and returns the rounded
// download the public pages state. The rounding rule lives here once.
func deriveFacts(root string) (Facts, error) {
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(CatalogPath)))
	if err != nil {
		return Facts{}, fmt.Errorf("read %s: %w", CatalogPath, err)
	}
	var catalog runtimeCatalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return Facts{}, fmt.Errorf("parse %s: %w", CatalogPath, err)
	}
	runtime, ok := catalog.Runtimes[catalog.LocalEmbedder.Runtime]
	if !ok {
		return Facts{}, fmt.Errorf("%s has no runtime %q", CatalogPath, catalog.LocalEmbedder.Runtime)
	}
	model, ok := catalog.Models[catalog.LocalEmbedder.Model]
	if !ok {
		return Facts{}, fmt.Errorf("%s has no model %q", CatalogPath, catalog.LocalEmbedder.Model)
	}
	total := func(platform string) (int64, error) {
		entry, ok := runtime.Platforms[platform]
		if !ok {
			return 0, fmt.Errorf("%s has no platform %q", CatalogPath, platform)
		}
		names := make([]string, 0, len(entry.Variants))
		for name := range entry.Variants {
			names = append(names, name)
		}
		if len(names) != 1 {
			return 0, fmt.Errorf("%s platform %q has %d variants, want one managed variant", CatalogPath, platform, len(names))
		}
		variant := entry.Variants[names[0]]
		var bytes int64
		for _, artifact := range variant.Artifacts {
			bytes += artifact.Size
		}
		return bytes + model.Size, nil
	}
	win, err := total("windows/amd64")
	if err != nil {
		return Facts{}, err
	}
	linux, err := total("linux/amd64")
	if err != nil {
		return Facts{}, err
	}
	return Facts{
		RuntimeWindowsMB: roundMB(win),
		RuntimeLinuxMB:   roundMB(linux),
	}, nil
}

// roundMB is the one public rounding rule for a download size.
func roundMB(bytes int64) int { return int((bytes + 500_000) / 1_000_000) }

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
	if !strings.HasPrefix(s.DocsSite, "https://") {
		problems = append(problems, "docs_site must be an https URL")
	}
	if !strings.HasPrefix(s.BaseURL, "/") {
		problems = append(problems, "baseurl must start with /")
	}
	if s.Facts.RuntimeWindowsMB <= 0 || s.Facts.RuntimeLinuxMB <= 0 {
		problems = append(problems, "runtime facts are missing")
	}
	require("hero.eyebrow", s.Hero.Eyebrow)
	require("hero.title", s.Hero.Title)
	require("hero.promise", s.Hero.Promise)
	require("hero.lede", s.Hero.Lede)
	require("hero.banner", s.Hero.Banner)
	require("hero.note", s.Hero.Note)
	require("hero.figure_alt", s.Hero.FigureAlt)
	require("hero.figure_caption", s.Hero.FigureCaption)
	require("differences.title", s.Differences.Title)
	require("differences.lead", s.Differences.Lead)
	if len(s.Differences.Items) != 3 {
		problems = append(problems, fmt.Sprintf("differences.items has %d items, want three", len(s.Differences.Items)))
	}
	for index, item := range s.Differences.Items {
		require(fmt.Sprintf("differences.items[%d].tag", index), item.Tag)
		require(fmt.Sprintf("differences.items[%d].title", index), item.Title)
		require(fmt.Sprintf("differences.items[%d].body", index), item.Body)
	}
	require("boundary.title", s.Boundary.Title)
	require("boundary.lead", s.Boundary.Lead)
	require("boundary.leaves_title", s.Boundary.LeavesTitle)
	require("boundary.stays_title", s.Boundary.StaysTitle)
	if len(s.Boundary.Leaves) == 0 {
		problems = append(problems, "boundary.leaves is empty")
	}
	for index, item := range s.Boundary.Leaves {
		require(fmt.Sprintf("boundary.leaves[%d]", index), item)
	}
	if len(s.Boundary.Stays) == 0 {
		problems = append(problems, "boundary.stays is empty")
	}
	for index, item := range s.Boundary.Stays {
		require(fmt.Sprintf("boundary.stays[%d]", index), item)
	}
	require("boundary.note", s.Boundary.Note)
	require("limits.title", s.Limits.Title)
	require("limits.lead", s.Limits.Lead)
	if len(s.Limits.Items) == 0 {
		problems = append(problems, "limits.items is empty")
	}
	for index, item := range s.Limits.Items {
		require(fmt.Sprintf("limits.items[%d].title", index), item.Title)
		require(fmt.Sprintf("limits.items[%d].body", index), item.Body)
	}
	require("install.title", s.Install.Title)
	require("install.lead", s.Install.Lead)
	require("install.windows", s.Install.Windows)
	require("install.linux", s.Install.Linux)
	require("install.command_note", s.Install.CommandNote)
	require("install.first_run", s.Install.FirstRun)
	require("install.note", s.Install.Note)
	if strings.TrimSpace(s.Install.Command) == "" {
		problems = append(problems, "install.command is empty")
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
	facts := s.factMap()
	placeholders := regexp.MustCompile(`\{([a-z_]+)\}`)
	checkText := func(name, text string) {
		for _, match := range placeholders.FindAllStringSubmatch(text, -1) {
			if _, ok := facts[match[1]]; !ok {
				problems = append(problems, fmt.Sprintf("%s: placeholder {%s} has no value", name, match[1]))
			}
		}
	}
	for name, text := range s.localizedText() {
		checkText(name, text)
	}
	checkText("install.command", s.Install.Command)
	checkText("build.commands", s.Build.Commands)
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// localizedText is every public string that can carry a placeholder. It is
// built here so a new section cannot skip the validator.
func (s Site) localizedText() map[string]string {
	out := map[string]string{}
	add := func(name string, value Localized) {
		out[name+".en"] = value.EN
		out[name+".fr"] = value.FR
	}
	add("hero.eyebrow", s.Hero.Eyebrow)
	add("hero.title", s.Hero.Title)
	add("hero.promise", s.Hero.Promise)
	add("hero.lede", s.Hero.Lede)
	add("hero.banner", s.Hero.Banner)
	add("hero.note", s.Hero.Note)
	add("hero.figure_alt", s.Hero.FigureAlt)
	add("hero.figure_caption", s.Hero.FigureCaption)
	add("differences.title", s.Differences.Title)
	add("differences.lead", s.Differences.Lead)
	for index, item := range s.Differences.Items {
		add(fmt.Sprintf("differences.items[%d].tag", index), item.Tag)
		add(fmt.Sprintf("differences.items[%d].title", index), item.Title)
		add(fmt.Sprintf("differences.items[%d].body", index), item.Body)
	}
	add("boundary.title", s.Boundary.Title)
	add("boundary.lead", s.Boundary.Lead)
	add("boundary.leaves_title", s.Boundary.LeavesTitle)
	add("boundary.stays_title", s.Boundary.StaysTitle)
	add("boundary.note", s.Boundary.Note)
	for index, item := range s.Boundary.Leaves {
		add(fmt.Sprintf("boundary.leaves[%d]", index), item)
	}
	for index, item := range s.Boundary.Stays {
		add(fmt.Sprintf("boundary.stays[%d]", index), item)
	}
	add("limits.title", s.Limits.Title)
	add("limits.lead", s.Limits.Lead)
	for index, item := range s.Limits.Items {
		add(fmt.Sprintf("limits.items[%d].title", index), item.Title)
		add(fmt.Sprintf("limits.items[%d].body", index), item.Body)
	}
	add("install.title", s.Install.Title)
	add("install.lead", s.Install.Lead)
	add("install.windows", s.Install.Windows)
	add("install.linux", s.Install.Linux)
	add("install.command_note", s.Install.CommandNote)
	add("install.first_run", s.Install.FirstRun)
	add("install.note", s.Install.Note)
	add("status.title", s.Status.Title)
	add("status.body", s.Status.Body)
	add("build.title", s.Build.Title)
	add("build.requirements", s.Build.Requirements)
	add("build.note", s.Build.Note)
	add("license.title", s.License.Title)
	add("license.body", s.License.Body)
	for index, item := range s.Nav {
		add(fmt.Sprintf("nav[%d].label", index), item.Label)
	}
	return out
}

// factMap is the one placeholder value table.
func (s Site) factMap() map[string]string {
	out := map[string]string{
		"license":         LicenseID,
		"repository":      s.Repository,
		"docs":            s.DocsSite,
		"runtime_windows": fmt.Sprintf("%d MB", s.Facts.RuntimeWindowsMB),
		"runtime_linux":   fmt.Sprintf("%d MB", s.Facts.RuntimeLinuxMB),
	}
	for _, item := range s.Nav {
		out[item.Key] = item.Key
	}
	return out
}

// text replaces every {key} placeholder with the documentation destination.
func (s Site) text(value string, lang string) string {
	return s.textFor(value, lang, s.docLinks())
}

// textFor replaces every {key} placeholder with the destination set the caller
// owns. The README needs repository-root paths, the docs pages need sibling
// names, and both read the same source text.
func (s Site) textFor(value string, lang string, links map[string]string) string {
	replacements := map[string]string{
		"license":         LicenseID,
		"repository":      s.Repository,
		"docs":            s.DocsSite,
		"runtime_windows": fmt.Sprintf("%d %s", s.Facts.RuntimeWindowsMB, mbLabel(lang)),
		"runtime_linux":   fmt.Sprintf("%d %s", s.Facts.RuntimeLinuxMB, mbLabel(lang)),
	}
	for key, href := range links {
		replacements[key] = href
	}
	pairs := make([]string, 0, len(replacements)*2)
	for key, value := range replacements {
		pairs = append(pairs, "{"+key+"}", value)
	}
	return strings.NewReplacer(pairs...).Replace(value)
}

func mbLabel(lang string) string {
	if lang == "fr" {
		return "Mo"
	}
	return "MB"
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
// pages in docs/site.json order, then the generated assets and configuration.
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
		Page{Key: "queue", EN: "docs/assets/queue-en.svg", FR: "docs/assets/queue-fr.svg",
			Render: func(site Site, lang string) []byte { return site.Queue(lang) }},
		Page{Key: "mark", EN: "docs/assets/mark.svg", FR: "docs/assets/mark.svg",
			Render: func(site Site, _ string) []byte { return site.Mark() }},
		Page{Key: "logo", EN: "docs/assets/logo.svg", FR: "docs/assets/logo.svg",
			Render: func(site Site, _ string) []byte { return site.Logo() }},
		Page{Key: "banner", EN: "docs/assets/banner-en.svg", FR: "docs/assets/banner-fr.svg",
			Render: func(site Site, lang string) []byte { return site.Banner(lang) }},
		Page{Key: "stylesheet", EN: "docs/assets/main.scss", FR: "docs/assets/main.scss",
			Render: func(site Site, _ string) []byte { return site.Stylesheet() }},
		Page{Key: "config", EN: "docs/_config.yml", FR: "docs/_config.yml",
			Render: func(site Site, _ string) []byte { return site.Config() }},
	)
	for index := range pages {
		if pages[index].Label == (Localized{}) {
			for _, item := range s.Nav {
				if item.Key == pages[index].Key {
					pages[index].Label = item.Label
				}
			}
		}
	}
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

// ManagedPaths is the authored public pages whose navigation and footer blocks
// gensite owns. They are Markdown files with authored content around two
// generated blocks.
func (s Site) ManagedPaths() []string {
	var out []string
	for _, page := range s.Pages() {
		if page.Render != nil || !page.Public {
			continue
		}
		out = append(out, page.File("en"), page.File("fr"))
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

// Write renders every generated output and updates the managed blocks in the
// authored pages. It returns the paths it wrote.
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
		if err := writeFile(root, name, body); err != nil {
			return written, err
		}
		written = append(written, name)
	}
	for _, name := range value.ManagedPaths() {
		target := filepath.Join(root, filepath.FromSlash(name))
		current, err := os.ReadFile(target)
		if err != nil {
			return written, fmt.Errorf("read %s: %w", name, err)
		}
		body, err := value.applyManaged(name, current)
		if err != nil {
			return written, err
		}
		if err := writeFile(root, name, body); err != nil {
			return written, err
		}
		written = append(written, name)
	}
	return written, nil
}

func writeFile(root, name string, body []byte) error {
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
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
	for _, name := range value.ManagedPaths() {
		current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			stale = append(stale, name)
			continue
		}
		body, err := value.applyManaged(name, current)
		if err != nil {
			return stale, err
		}
		if !bytes.Equal(current, body) {
			stale = append(stale, name)
		}
	}
	return stale, nil
}

// excludedDocs derives the Jekyll exclude list from the public page table and
// the files actually present. A new internal document is excluded without a
// hand edit, and a generated configuration cannot publish it by accident.
func excludedDocs(root string, s Site) []string {
	public := map[string]bool{}
	for _, page := range s.Pages() {
		if page.Public {
			public[page.EN] = true
			public[page.FR] = true
		}
	}
	out := []string{"site.json"}
	for _, dir := range []string{"docs", "docs" + string(filepath.Separator) + "fr"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			continue
		}
		relDir := strings.TrimPrefix(filepath.ToSlash(dir), "docs")
		relDir = strings.TrimPrefix(relDir, "/")
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			rel := path.Join(relDir, entry.Name())
			if !public[path.Join("docs", rel)] {
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out
}
