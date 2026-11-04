package mail

import (
	"strings"
	"testing"
)

var (
	benchStringSink string
	benchIntSink    int
)

// benchTextBody is the retention-sensitive shape: about 1.1 MB of prose with
// line feeds.
func benchTextBody() string {
	return strings.Repeat("The quick brown fox jumps over the lazy dog.\n", 25000)
}

func BenchmarkExcerptLargeBody(b *testing.B) {
	body := benchTextBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchStringSink = Excerpt(body, 240)
	}
}

func BenchmarkWordCount(b *testing.B) {
	body := benchTextBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchIntSink = WordCount(body)
	}
}
