package contacts

import "testing"

func TestCleanPhoneKeepsDisplayFormAndBoundsIt(t *testing.T) {
	cases := map[string]string{
		"  +1 (555) 010-0100  ":      "+1 (555) 010-0100",
		"+33 1 23 45 67 89 poste 12": "+33 1 23 45 67 89 poste 12",
		"\n+1 555\t010\r\n":          "+1 555010",
		"":                           "",
		"  ":                         "",
		"+1 555 010 0100\x00\x07":    "+1 555 010 0100",
	}
	for in, want := range cases {
		if got := CleanPhone(in); got != want {
			t.Errorf("CleanPhone(%q) = %q, want %q", in, got, want)
		}
	}
	long := ""
	for i := 0; i < 100; i++ {
		long += "9"
	}
	if got := CleanPhone(long); len([]rune(got)) != maxPhoneRunes {
		t.Fatalf("over-long phone kept %d runes", len([]rune(got)))
	}
}
