package site

import (
	"fmt"
	"html"
	"strings"
	"testing"
)

// TestAnswerSlipCitesTheCaptureFixture ties the first-viewport slip to the one
// capture fixture. A subject, sender or instant that drifted from data.ts would
// put a fact on the page the product does not show.
func TestAnswerSlipCitesTheCaptureFixture(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	fixture := readDoc(t, root(t), "e2e/screenshots/data.ts")
	for index, citation := range value.Hero.Answer.Citations {
		for _, want := range []string{
			citation.Subject.EN,
			`sender: "` + citation.Sender + `"`,
			`"` + citation.At + `"`,
		} {
			if !strings.Contains(fixture, want) {
				t.Errorf("citations[%d] names %q, which is absent from e2e/screenshots/data.ts", index, want)
			}
		}
		if citation.Date.EN == "" || citation.Date.FR == "" {
			t.Errorf("citations[%d] has no display date in both languages", index)
		}
	}
}

// TestLandingScreenIndexIsCSSOnly guards the signature interaction: one radio,
// one label and one figure per media row, exactly one eager image, no script,
// and one stage rule per screen in the one stylesheet.
func TestLandingScreenIndexIsCSSOnly(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "fr"} {
		landing := html.UnescapeString(string(value.Landing(lang)))
		if strings.Contains(landing, "<script") {
			t.Errorf("%s landing carries a script; the screen index must be CSS only", lang)
		}
		if got, want := strings.Count(landing, `type="radio"`), len(value.Media.Screenshots); got != want {
			t.Errorf("%s landing has %d radios, want %d", lang, got, want)
		}
		if got, want := strings.Count(landing, `class="screen-tab"`), len(value.Media.Screenshots); got != want {
			t.Errorf("%s landing has %d labels, want %d", lang, got, want)
		}
		if got, want := strings.Count(landing, `<figure class="shot"`), len(value.Media.Screenshots); got != want {
			t.Errorf("%s landing has %d figures, want %d", lang, got, want)
		}
		if got := strings.Count(landing, `loading="eager"`); got != 1 {
			t.Errorf("%s landing has %d eager images, want one", lang, got)
		}
		if got, want := strings.Count(landing, `loading="lazy"`), len(value.Media.Screenshots)-1; got != want {
			t.Errorf("%s landing has %d lazy images, want %d", lang, got, want)
		}
		if got := strings.Count(landing, `class="screen-radio"`); got != len(value.Media.Screenshots) {
			t.Errorf("%s landing has %d visually hidden radios, want %d", lang, got, len(value.Media.Screenshots))
		}
		for _, shot := range value.Media.Screenshots {
			for _, want := range []string{
				fmt.Sprintf(`id="screen-%s"`, shot.Key),
				fmt.Sprintf(`id="shot-%s"`, shot.Key),
				fmt.Sprintf(`for="screen-%s"`, shot.Key),
				shot.Caption.For(lang),
			} {
				if !strings.Contains(landing, want) {
					t.Errorf("%s landing is missing %q", lang, want)
				}
			}
		}
	}
	css := string(value.Stylesheet())
	for _, shot := range value.Media.Screenshots {
		rule := fmt.Sprintf("#screen-%s:checked) #shot-%s", shot.Key, shot.Key)
		if !strings.Contains(css, rule) {
			t.Errorf("the stylesheet has no stage rule for %q", shot.Key)
		}
	}
	if !strings.Contains(css, fmt.Sprintf("aspect-ratio: %d / %d", value.Media.ScreenshotWidth, value.Media.ScreenshotHeight)) {
		t.Error("the stylesheet does not reserve the capture aspect ratio")
	}
}

// TestLandingAnswerSlipIsHonest guards the slip's sample label and its claim:
// the static rows carry no chevron because they cannot open anything, and the
// caption scopes the behaviour to the app.
func TestLandingAnswerSlipIsHonest(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "fr"} {
		landing := html.UnescapeString(string(value.Landing(lang)))
		for _, want := range []string{
			`class="slip-sample"`,
			value.Hero.Answer.Sample.For(lang),
			value.Hero.Answer.Caption.For(lang),
		} {
			if !strings.Contains(landing, want) {
				t.Errorf("%s landing is missing %q", lang, want)
			}
		}
		if got := strings.Count(landing, `class="citation-open"`); got != 0 {
			t.Errorf("%s landing still renders %d citation chevrons", lang, got)
		}
		if got := strings.Count(landing, "›"); got != 0 {
			t.Errorf("%s landing still shows %d chevron characters", lang, got)
		}
	}
}

// TestFrenchLandingHasNoEnglishSampleField guards the localization path for
// every invented sample fact, including citation subjects and queue rows.
func TestFrenchLandingHasNoEnglishSampleField(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	type pair struct {
		name  string
		value Localized
	}
	pairs := []pair{
		{"hero.answer.sample", value.Hero.Answer.Sample},
		{"hero.answer.caption", value.Hero.Answer.Caption},
		{"media.view_full", value.Media.ViewFull},
		{"media.index_hint_touch", value.Media.IndexHintTouch},
	}
	for index, citation := range value.Hero.Answer.Citations {
		pairs = append(pairs, pair{fmt.Sprintf("hero.answer.citations[%d].subject", index), citation.Subject})
	}
	for index, item := range value.Differences.Queue.Items {
		pairs = append(pairs,
			pair{fmt.Sprintf("differences.queue.items[%d].subject", index), item.Subject},
			pair{fmt.Sprintf("differences.queue.items[%d].action", index), item.Action},
		)
	}
	fr := html.UnescapeString(string(value.Landing("fr")))
	for _, item := range pairs {
		if !strings.Contains(fr, item.value.FR) {
			t.Errorf("French landing is missing %s: %q", item.name, item.value.FR)
		}
		if item.value.EN == item.value.FR {
			continue
		}
		if strings.Contains(fr, item.value.EN) {
			t.Errorf("French landing leaks the English %s: %q", item.name, item.value.EN)
		}
	}
}

// TestLandingQueueIsRankedAndInvented guards the second proof: a dense table
// with a rank, a subject, a sender and one action value, placed before the
// three mechanism columns, with no model score shown and no action restated.
func TestLandingQueueIsRankedAndInvented(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	queue := value.Differences.Queue
	for _, lang := range []string{"en", "fr"} {
		landing := html.UnescapeString(string(value.Landing(lang)))
		if got, want := strings.Count(landing, `<table class="queue-table">`), 1; got != want {
			t.Errorf("%s landing has %d queue tables, want %d", lang, got, want)
			continue
		}
		tableAt := strings.Index(landing, `class="queue-table"`)
		bandAt := strings.Index(landing, `class="queue-band"`)
		if tableAt < 0 || bandAt < 0 || tableAt > bandAt {
			t.Errorf("%s landing does not place the queue table before the mechanism columns", lang)
		}
		if !strings.Contains(landing, queue.Label.For(lang)) {
			t.Errorf("%s landing is missing the invented queue label", lang)
		}
		for _, column := range queue.Columns {
			if !strings.Contains(landing, column.For(lang)) {
				t.Errorf("%s landing is missing queue heading %q", lang, column.For(lang))
			}
		}
		for index, item := range queue.Items {
			for _, want := range []string{
				item.Subject.For(lang),
				item.Sender,
				fmt.Sprintf(`<td class="queue-action">%s</td>`, item.Action.For(lang)),
				fmt.Sprintf(`class="queue-rank">%02d<`, index+1),
			} {
				if !strings.Contains(landing, want) {
					t.Errorf("%s landing queue row %d is missing %q", lang, index, want)
				}
			}
		}
		if got, want := strings.Count(landing, `class="queue-rank"`), len(queue.Items); got != want {
			t.Errorf("%s landing has %d ranked rows, want %d", lang, got, want)
		}
		if got, want := strings.Count(landing, `<td class="queue-action">`), len(queue.Items); got != want {
			t.Errorf("%s landing has %d action cells, want %d", lang, got, want)
		}
		if strings.Contains(landing, `class="queue-reason"`) || strings.Contains(landing, `class="queue-verb"`) {
			t.Errorf("%s landing still restates the action in a second queue span", lang)
		}
	}
}

// TestLandingScreensOfferFullSize guards the enlarge path and the two hints.
func TestLandingScreensOfferFullSize(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "fr"} {
		landing := html.UnescapeString(string(value.Landing(lang)))
		if got, want := strings.Count(landing, `class="shot-full"`), len(value.Media.Screenshots); got != want {
			t.Errorf("%s landing has %d full-size links, want %d", lang, got, want)
		}
		if got, want := strings.Count(landing, `target="_blank" rel="noopener"`), len(value.Media.Screenshots); got != want {
			t.Errorf("%s landing has %d new-tab links, want %d", lang, got, want)
		}
		for _, want := range []string{
			value.Media.ViewFull.For(lang),
			value.Media.IndexHint.For(lang),
			value.Media.IndexHintTouch.For(lang),
		} {
			if !strings.Contains(landing, want) {
				t.Errorf("%s landing is missing %q", lang, want)
			}
		}
	}
	css := string(value.Stylesheet())
	if strings.Contains(css, "opacity: 0.45") {
		t.Error("the stylesheet still fades the 01/02/03 numerals")
	}
}
