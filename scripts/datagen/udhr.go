package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/locale/ngram"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// langProfile is one language of langid.json.
type langProfile struct {
	Tag     string   `json:"tag"`    // BCP 47 form of the CLDR locale id
	Locale  string   `json:"locale"` // the CLDR locale id (locales.json key is Tag)
	Texts   []string `json:"udhr"`   // the UDHR translations it was trained on (file keys)
	Scripts []string `json:"scripts"`
	// Letters and Auxiliary are the CLDR exemplar letters of the locale:
	// the ones its writing uses, and the ones it borrows.
	Letters   string   `json:"letters"`
	Auxiliary string   `json:"auxiliary"`
	Profile   []string `json:"profile"`
}

type udhrEntry struct {
	File   string `xml:"f,attr"`
	BCP47  string `xml:"bcp47,attr"`
	Stage  string `xml:"stage,attr"`
	Status string `xml:"status,attr"`
}

// udhrProfiles trains the profile of every CLDR locale from ALL its UDHR
// translations of the minimum stage and a chosen status (spelling
// reforms, regional and monotonic/polytonic texts of one language
// together; dialect texts filed under its tag left out).
func udhrProfiles(dir string, ids []string, sup *supplemental, p localeParams) ([]langProfile, error) {
	var index struct {
		Entries []udhrEntry `xml:"udhr"`
	}
	if err := decodeXML(filepath.Join(dir, "index.xml"), &index); err != nil {
		return nil, err
	}
	var out []langProfile
	for _, id := range ids {
		var texts []string
		var files []string
		for _, e := range index.Entries {
			if stage, err := strconv.Atoi(e.Stage); err != nil || stage < p.UDHRMinStage || !slices.Contains(p.UDHRStatus, e.Status) ||
				!udhrMatches(e.BCP47, id, sup) {
				continue
			}
			text, err := udhrText(filepath.Join(dir, "udhr_"+e.File+".xml"))
			if err != nil {
				return nil, err
			}
			texts, files = append(texts, text), append(files, e.File)
		}
		if len(texts) == 0 {
			continue
		}
		text := strings.Join(texts, "\n")
		out = append(out, langProfile{Tag: strings.ReplaceAll(id, "_", "-"), Locale: id, Texts: files,
			Scripts: scriptsOf(text, p.MinScriptShare), Profile: ngram.Top(ngram.Count(ngram.Normalize(text), p.NgramMax), p.ProfileSize)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("udhr: no translation matched the %d CLDR locales", len(ids))
	}
	return out, nil
}

// udhrMatches reports whether a UDHR translation's BCP 47 tag is of a
// CLDR locale (BCP 47 lookup): its language subtag, canonicalized by
// CLDR's language aliases (macrolanguages: arb is ar, cmn is zh), is the
// locale's language, and its script subtag is the locale's script (the
// explicit one, else the language's likely script); a tag without a
// script subtag matches only the locale without one. Region and variant
// subtags do not matter (pt-BR and pt-PT are Portuguese).
func udhrMatches(tag, id string, sup *supplemental) bool {
	t, l := strings.Split(tag, "-"), strings.Split(id, "_")
	lang := t[0]
	if a, ok := sup.aliases[lang]; ok {
		lang = a
	}
	if lang != l[0] {
		return false
	}
	tScript, lScript := "", ""
	if len(t) > 1 && len(t[1]) == 4 && !isDigits(t[1]) {
		tScript = t[1]
	}
	if len(l) > 1 && len(l[1]) == 4 {
		lScript = l[1]
	}
	switch {
	case tScript == "":
		return lScript == ""
	case lScript != "":
		return tScript == lScript
	}
	parts := strings.Split(sup.likely[l[0]], "_")
	return len(parts) >= 2 && parts[1] == tScript
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// udhrText is the character data of a translation (every element).
func udhrText(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return b.String(), nil
		}
		if err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		if cd, ok := tok.(xml.CharData); ok {
			b.Write(cd)
			b.WriteByte('\n')
		}
	}
}

// scriptsOf lists the scripts holding at least share of the letters.
func scriptsOf(text string, share float64) []string {
	p := mail.Scripts(text)
	var out []string
	for name, n := range p.ByScript {
		if name != "" && float64(n) >= share*float64(p.Letters) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
