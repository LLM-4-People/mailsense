package mail

import (
	"testing"
	"unicode"
)

func TestScriptRulesAreValid(t *testing.T) {
	for name := range scripts().substring {
		if unicode.Scripts[name] == nil {
			t.Errorf("scripts.json: %q is not a Unicode script name", name)
		}
	}
	for name := range scripts().spaceless {
		if !scripts().substring[name] {
			t.Errorf("scripts.json: spaceless %q must be searched by substring", name)
		}
		if unicode.Scripts[name] == nil {
			t.Errorf("scripts.json: %q is not a Unicode script name", name)
		}
	}
}

func TestScripts(t *testing.T) {
	for r, want := range map[rune]string{'a': "Latin", 'é': "Latin", 'Ж': "Cyrillic", 'Ω': "Greek", 'ب': "Arabic", 'א': "Hebrew",
		'中': "Han", 'か': "Hiragana", 'カ': "Katakana", '한': "Hangul", 'ไ': "Thai", '1': "Common", '😀': "Common", '𠀀': "Han"} {
		if got := ScriptOf(r); got != want {
			t.Errorf("ScriptOf(%q) = %q, want %q", r, got, want)
		}
	}
	p := Scripts("请在星期五之前发送 report")
	if p.Dominant() != "Han" || p.Letters != 15 || p.Cased != 6 || p.SpacelessShare() < 0.6 {
		t.Fatalf("%+v %s %v", p, p.Dominant(), p.SpacelessShare())
	}
	if !Spaceless("Thai") || Spaceless("Latin") || Spaceless("Hangul") {
		t.Fatal("Spaceless")
	}
	if !NeedsSubstringSearch("x 견적서") || !NeedsSubstringSearch("ไทย") || NeedsSubstringSearch("Ελληνικά и русский") {
		t.Fatal("NeedsSubstringSearch")
	}
	if (Scripts("123 !?").Dominant()) != "" || Scripts("").SpacelessShare() != 0 {
		t.Fatal("no letters, no script")
	}
}
