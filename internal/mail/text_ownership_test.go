package mail

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"
)

// legacyHead is the pre-fix Head: it returned a slice of s unchanged.
func legacyHead(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// legacyExcerpt is the pre-fix Excerpt: it copied the whole body before
// cutting it.
func legacyExcerpt(body string, n int) string {
	return legacyHead(strings.ReplaceAll(Strip(body), "\n", " "), n)
}

// aliases reports whether out's first byte lies in input's backing array.
func aliases(out, input string) bool {
	if len(out) == 0 || len(input) == 0 {
		return false
	}
	o := uintptr(unsafe.Pointer(unsafe.StringData(out)))
	in := uintptr(unsafe.Pointer(unsafe.StringData(input)))
	return o >= in && o < in+uintptr(len(input))
}

func TestHeadDoesNotAliasTheInput(t *testing.T) {
	body := strings.Repeat("0123456789abcdef", 1<<12)
	for _, n := range []int{1, 2, 10, 239, 240, 1000} {
		out := Head(body, n)
		if aliases(out, body) {
			t.Fatalf("Head(body, %d) aliases the %d-byte input", n, len(body))
		}
		if out != legacyHead(body, n) {
			t.Fatalf("Head(body, %d) = %q, want %q", n, out, legacyHead(body, n))
		}
	}
}

func TestExcerptDoesNotAliasTheInput(t *testing.T) {
	// No leading/trailing whitespace and no line feed: the old
	// strings.ReplaceAll returned the input itself, so the old Head result
	// was a slice of body.
	body := strings.Repeat("abcdefghij", 1<<10)
	for _, n := range []int{1, 10, 240, 1000} {
		out := Excerpt(body, n)
		if aliases(out, body) {
			t.Fatalf("Excerpt(body, %d) aliases the %d-byte input", n, len(body))
		}
		if out != legacyExcerpt(body, n) {
			t.Fatalf("Excerpt(body, %d) = %q, want %q", n, out, legacyExcerpt(body, n))
		}
	}
}

func TestExcerptOwnsOnlyItsRunes(t *testing.T) {
	const line = "The quick brown fox jumps over the lazy dog.\n"
	body := strings.Repeat(line, (1<<20)/len(line)+1)
	if len(body) < 1<<20 {
		t.Fatalf("body is only %d bytes", len(body))
	}
	const n = 240
	want := legacyExcerpt(body, n)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	out := Excerpt(body, n)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(out)
	runtime.KeepAlive(body)

	if out != want {
		t.Fatalf("Excerpt = %q, want %q", out, want)
	}
	if got := utf8.RuneCountInString(out); got > n {
		t.Fatalf("Excerpt kept %d code points, want at most %d", got, n)
	}
	if aliases(out, body) {
		t.Fatal("Excerpt aliases its input")
	}
	// One owned allocation of the excerpt, not a full-body copy. The bound
	// leaves room for the builder, runtime accounting, and a multibyte
	// excerpt; the old implementation allocated the whole 1 MiB body.
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 128<<10 {
		t.Fatalf("Excerpt allocated %d bytes for a %d-byte body, want an excerpt-sized allocation", alloc, len(body))
	}
}

func TestWordCountMatchesWords(t *testing.T) {
	cases := []string{
		"",
		" ",
		"\n\t\r",
		"a",
		" a b ",
		"a\u00a0b\x1cc  d",
		"é😀 x",
		"a\n\nb",
		"a\u3000b\u2028c",
		strings.Repeat("word ", 1000),
	}
	for _, s := range cases {
		if got, want := WordCount(s), len(Words(s)); got != want {
			t.Fatalf("WordCount(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestHeadAndExcerptMatchLegacyOutput(t *testing.T) {
	bodies := []string{
		"",
		" ",
		"\n\n",
		"  hi\nthere\n ",
		"é😀x\nsecond line",
		"a\r\nb",
		string([]byte{0xff, 'a', '\n', 0xfe, 'b'}),
		strings.Repeat("x\n", 1000),
	}
	limits := []int{-1, 0, 1, 2, 3, 7, 10, 240, 1 << 20}
	for _, body := range bodies {
		for _, n := range limits {
			if got, want := Head(body, n), legacyHead(body, n); got != want {
				t.Fatalf("Head(%q, %d) = %q, want %q", body, n, got, want)
			}
			if got, want := Excerpt(body, n), legacyExcerpt(body, n); got != want {
				t.Fatalf("Excerpt(%q, %d) = %q, want %q", body, n, got, want)
			}
		}
	}
}
