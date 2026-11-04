package mail

import "testing"

func TestInlineImageTypeNeverTrustsANameOverItsType(t *testing.T) {
	cases := []struct {
		mediaType, name, want string
	}{
		{"image/png", "logo.png", "image/png"},
		{"IMAGE/JPEG; charset=binary", "photo.jpg", "image/jpeg"},
		{"image/webp", "x", "image/webp"},
		{"image/jpg", "photo.jpg", "image/jpeg"},
		{"image/pjpeg", "", "image/jpeg"},
		{"image/x-png", "logo", "image/png"},
		{"IMAGE/X-PNG; charset=binary", "", "image/png"},
		{"application/octet-stream", "logo.png", "image/png"},
		{"", "photo.JPG", "image/jpeg"},
		{"", "animation.gif", "image/gif"},
		{"image/svg+xml", "logo.png", ""},
		{"image/svg+xml", "logo.svg", ""},
		{"image/x-icon", "favicon.ico", ""},
		{"text/html", "logo.png", ""},
		{"application/pdf", "report.pdf", ""},
		{"", "archive.zip", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := InlineImageType(tc.mediaType, tc.name); got != tc.want {
			t.Errorf("InlineImageType(%q, %q) = %q; want %q", tc.mediaType, tc.name, got, tc.want)
		}
	}
}

func TestInlineImageTypeFromBytes(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n0000")
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}
	gif87 := []byte("GIF87a0000")
	gif89 := []byte("GIF89a0000")
	webp := []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"png", png, "image/png"},
		{"jpeg", jpeg, "image/jpeg"},
		{"gif87", gif87, "image/gif"},
		{"gif89", gif89, "image/gif"},
		{"webp", webp, "image/webp"},
		{"truncated", []byte{0xff, 0xd8}, ""},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), ""},
		{"pdf", []byte("%PDF-1.7"), ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		if got := InlineImageTypeFromBytes(tc.data); got != tc.want {
			t.Errorf("%s: InlineImageTypeFromBytes = %q; want %q", tc.name, got, tc.want)
		}
	}
}

func TestInlineImageTypeNeedsBytes(t *testing.T) {
	cases := []struct {
		mediaType, name string
		want            bool
	}{
		{"application/octet-stream", "image001.bin", true},
		{"application/octet-stream", "logo.png", false},
		{"", "attachment", true},
		{"image/jpg", "photo.jpg", false},
		{"application/pdf", "report.pdf", false},
		{"image/svg+xml", "logo.svg", false},
	}
	for _, tc := range cases {
		if got := InlineImageTypeNeedsBytes(tc.mediaType, tc.name); got != tc.want {
			t.Errorf("InlineImageTypeNeedsBytes(%q, %q) = %v; want %v", tc.mediaType, tc.name, got, tc.want)
		}
	}
}

func TestNormalizeContentID(t *testing.T) {
	for raw, want := range map[string]string{
		"<logo@example.test>": "logo@example.test",
		" logo@example.test ": "logo@example.test",
		"<<weird>>":           "<weird>",
		"":                    "",
	} {
		if got := NormalizeContentID(raw); got != want {
			t.Errorf("NormalizeContentID(%q) = %q; want %q", raw, got, want)
		}
	}
}
