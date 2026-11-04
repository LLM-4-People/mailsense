package mail

import (
	"bytes"
	"mime"
	"path"
	"strings"
)

// imageTypeAlias folds the media-type spellings real mail carries onto the
// four raster types the renderer can display. image/jpg, image/pjpeg and
// image/x-png are common non-standard labels; treating them as unknown
// silently dropped the inline image they described.
func imageTypeAlias(mediaType string) string {
	t := strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0]))
	switch t {
	case "image/png", "image/x-png":
		return "image/png"
	case "image/jpeg", "image/jpg", "image/pjpeg":
		return "image/jpeg"
	case "image/gif", "image/x-gif":
		return "image/gif"
	case "image/webp", "image/x-webp":
		return "image/webp"
	}
	return ""
}

// NormalizeContentID is the bare MIME content id: HTML references it as
// cid:<id>, while the header carries it in angle brackets. It is the one
// normalisation the MIME and Outlook connectors and the renderer share.
func NormalizeContentID(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return strings.TrimSpace(s)
}

// InlineImageType returns the safe raster type of an inline part, inferring
// it from the file name when the part only says application/octet-stream.
// SVG and every non-raster image are deliberately excluded: an inline SVG is
// active content, not a picture. The renderer only ever resolves these four
// types, so the connectors and the renderer cannot disagree about what may be
// stored and displayed.
func InlineImageType(mediaType, name string) string {
	declared := strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0]))
	if t := imageTypeAlias(declared); t != "" {
		return t
	}
	// A name may fill in a missing or generic type, never override a type
	// that says something else: an SVG part named logo.png stays SVG and is
	// therefore refused.
	if declared != "" && declared != "application/octet-stream" {
		return ""
	}
	if ext := strings.ToLower(path.Ext(name)); ext != "" {
		return imageTypeAlias(mime.TypeByExtension(ext))
	}
	return ""
}

// InlineImageTypeNeedsBytes reports whether a part that its label and name do
// not identify as a raster image may still be one. Outlook labels many real
// inline pictures application/octet-stream with a .bin name, so a connector
// may read those bytes inside its existing budget and identify them with
// InlineImageTypeFromBytes. A declared non-generic type is trusted: a PDF is
// never read as a picture.
func InlineImageTypeNeedsBytes(mediaType, name string) bool {
	if InlineImageType(mediaType, name) != "" {
		return false
	}
	declared := strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0]))
	return declared == "" || declared == "application/octet-stream"
}

// InlineImageTypeFromBytes identifies a raster image by its magic number. It
// is the fallback for parts whose metadata is generic, and it accepts only
// the four types the renderer can display; the bytes are never trusted to
// name a different content type.
func InlineImageTypeFromBytes(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}
