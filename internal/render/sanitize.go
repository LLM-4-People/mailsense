package render

import (
	"errors"
	stdhtml "html"
	"net/url"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Safety caps. They bound the work one message can cause, not its stored
// size: a legal raw body is already capped by mail.body.html_max_chars.
const (
	MaxRawBytes    = 4 << 20 // 4 MiB of raw HTML (a 500k-unit body with 4-byte runes fits)
	MaxOutputBytes = 1 << 20 // 1 MiB of sanitized fragment
	MaxNodes       = 50000
	MaxDepth       = 256

	// maxDataImageBytes bounds an in-HTML data: image. A larger one is a
	// placeholder; it is never copied into the fragment.
	maxDataImageBytes = 384 << 10
)

// ErrTooLarge reports a body the renderer refuses to build (over a byte,
// node or nesting cap). The caller falls back to the escaped plain text.
var ErrTooLarge = errors.New("render: body exceeds display limits")

// Stats are the facts the API reports about images in one fragment.
type Stats struct {
	RemoteImages  int // remote images found
	BlockedImages int // remote images replaced or suppressed
	InlineImages  int // cid/location images resolved to stored bytes
	MissingInline int // inline references with no stored bytes
}

// options are the one extra input of the presentation compiler: the message's
// attachment identity. Bytes never enter the renderer; a SHA-256 is enough to
// tell the client which authenticated asset to hydrate.
type options struct {
	attachments []mail.Attachment
}

type fragment struct {
	html  string
	css   string
	stats Stats
}

// sanitize is the compatibility wrapper used by the unit tests and by
// callers that have no attachments.
func sanitize(raw string) (string, Stats, error) {
	f, err := build(raw, options{})
	return f.html, f.stats, err
}

// build parses raw HTML and rebuilds the allowed content, the compiled
// presentation sheet and the image facts.
func build(raw string, opt options) (fragment, error) {
	if len(raw) > MaxRawBytes {
		return fragment{}, ErrTooLarge
	}
	doc, err := xhtml.Parse(strings.NewReader(raw))
	if err != nil {
		return fragment{}, err
	}
	body := findBody(doc)
	if body == nil {
		return fragment{}, errors.New("render: no body")
	}
	w := &walker{stats: &Stats{}, out: &strings.Builder{}, css: newStyleSheet()}
	w.indexAttachments(opt.attachments)
	w.collectStyles(doc)
	w.bodyStyle(body)
	if err := w.children(body, 0); err != nil {
		return fragment{}, err
	}
	if w.out.Len() > MaxOutputBytes {
		return fragment{}, ErrTooLarge
	}
	return fragment{html: w.out.String(), css: w.css.text(), stats: *w.stats}, nil
}

type walker struct {
	stats *Stats
	out   *strings.Builder
	css   *styleSheet
	nodes int

	inline    map[string]int
	locations map[string]int
	knownCID  map[string]bool
	shas      []string // content address per attachment position
}

func (w *walker) indexAttachments(atts []mail.Attachment) {
	w.inline = map[string]int{}
	w.locations = map[string]int{}
	w.knownCID = map[string]bool{}
	w.shas = make([]string, len(atts))
	for i, a := range atts {
		w.shas[i] = a.SHA256
		if !a.Inline || mail.InlineImageType(a.ContentType, a.Name) == "" {
			continue
		}
		if cid := strings.ToLower(mail.NormalizeContentID(a.ContentID)); cid != "" {
			w.knownCID[cid] = true
			if a.SHA256 != "" {
				w.inline[cid] = i
			}
		}
		if loc := strings.ToLower(strings.TrimSpace(a.ContentLocation)); loc != "" && a.SHA256 != "" {
			w.locations[loc] = i
		}
	}
}

func findBody(n *xhtml.Node) *xhtml.Node {
	if n.Type == xhtml.ElementNode && strings.EqualFold(n.Data, "body") {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if b := findBody(c); b != nil {
			return b
		}
	}
	return nil
}

func (w *walker) write(s string) error {
	if w.out.Len()+len(s) > MaxOutputBytes {
		return ErrTooLarge
	}
	w.out.WriteString(s)
	return nil
}

func (w *walker) children(n *xhtml.Node, depth int) error {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := w.node(c, depth); err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) node(n *xhtml.Node, depth int) error {
	switch n.Type {
	case xhtml.TextNode:
		return w.write(stdhtml.EscapeString(n.Data))
	case xhtml.ElementNode:
		w.nodes++
		if w.nodes > MaxNodes {
			return ErrTooLarge
		}
		tag := strings.ToLower(n.Data)
		if tag == "style" {
			return nil // collected before the body walk
		}
		if dropElements[tag] || hidden(n) {
			return nil
		}
		if tag == "img" {
			return w.image(n)
		}
		mapped, ok := allowedElements[tag]
		if !ok {
			if depth >= MaxDepth {
				return ErrTooLarge
			}
			return w.children(n, depth+1) // unknown container: keep its text
		}
		if depth >= MaxDepth {
			return ErrTooLarge
		}
		if mapped == "a" {
			return w.anchor(n, depth)
		}
		if err := w.write("<" + mapped + w.attrs(n, tag) + ">"); err != nil {
			return err
		}
		if voidElements[mapped] {
			return nil
		}
		if err := w.children(n, depth+1); err != nil {
			return err
		}
		return w.write("</" + mapped + ">")
	case xhtml.CommentNode, xhtml.DoctypeNode:
		return nil
	default:
		return w.children(n, depth)
	}
}

func (w *walker) anchor(n *xhtml.Node, depth int) error {
	href := ""
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "href") {
			href = a.Val
			break
		}
	}
	link, ok := safeLink(href)
	if !ok {
		return w.children(n, depth+1) // keep the label, drop the unsafe link
	}
	open := `<a href="` + stdhtml.EscapeString(link) + `" target="_blank" rel="noopener noreferrer nofollow"` +
		w.globalAttrs(n) + w.styleAttr(n) + `>`
	if err := w.write(open); err != nil {
		return err
	}
	if err := w.children(n, depth+1); err != nil {
		return err
	}
	return w.write("</a>")
}

// image emits one of four things: a hydrated inline asset, a safe data image,
// a sized placeholder for a blocked remote image, or a compact "not shown"
// marker. A 1×1 remote image is a tracking pixel and is suppressed entirely.
func (w *walker) image(n *xhtml.Node) error {
	src := nodeAttr(n, "src")
	srcset := nodeAttr(n, "srcset")
	alt := visibleText(nodeAttr(n, "alt"))
	title := nodeAttr(n, "title")
	width := dimension(nodeAttr(n, "width"))
	height := dimension(nodeAttr(n, "height"))
	spec := w.resolveImage(src, srcset)
	switch spec.kind {
	case imageInline:
		class := "mail-inline-asset"
		if sender := safeClassList(nodeAttr(n, "class")); sender != "" {
			class += " " + sender
		}
		if box := w.css.box(width, height); box != "" {
			class += " " + box
		}
		attrs := ` class="` + class + `" data-inline="` + strconv.Itoa(spec.position) + `"`
		if spec.sha != "" {
			attrs += ` data-inline-v="` + stdhtml.EscapeString(spec.sha) + `"`
		}
		if id := safeFragmentID(nodeAttr(n, "id")); id != "" {
			attrs += ` id="` + stdhtml.EscapeString(id) + `"`
		}
		if alt != "" {
			attrs += ` role="img" aria-label="` + stdhtml.EscapeString(alt) + `"`
		}
		if title != "" {
			attrs += ` title="` + stdhtml.EscapeString(truncateRunes(title, 512)) + `"`
		}
		if align := safeAlign(nodeAttr(n, "align"), false); align != "" {
			attrs += ` data-image-align="` + align + `"`
		}
		w.stats.InlineImages++
		return w.write(`<span` + attrs + w.styleAttr(n) + `>` + stdhtml.EscapeString(alt) + `</span>`)
	case imageData:
		attrs := ` src="` + stdhtml.EscapeString(spec.url) + `" alt="` + stdhtml.EscapeString(alt) + `"`
		if width != "" {
			attrs += ` width="` + stdhtml.EscapeString(strings.TrimSuffix(width, "px")) + `"`
		}
		if height != "" {
			attrs += ` height="` + stdhtml.EscapeString(strings.TrimSuffix(height, "px")) + `"`
		}
		if title != "" {
			attrs += ` title="` + stdhtml.EscapeString(truncateRunes(title, 512)) + `"`
		}
		if sender := safeClassList(nodeAttr(n, "class")); sender != "" {
			attrs += ` class="` + stdhtml.EscapeString(sender) + `"`
		}
		if id := safeFragmentID(nodeAttr(n, "id")); id != "" {
			attrs += ` id="` + stdhtml.EscapeString(id) + `"`
		}
		return w.write(`<img` + attrs + w.styleAttr(n) + `>`)
	case imageRemote:
		w.stats.RemoteImages++
		w.stats.BlockedImages++
		if width == "1px" && height == "1px" {
			return nil // a tracking pixel, never a picture worth a box
		}
		return w.placeholder("Remote image blocked", "mail-image-blocked", alt, width, height, n)
	default:
		label := "Image not shown"
		class := "mail-image-missing"
		if spec.cid {
			label = "Inline image not available"
			class = "mail-image-inline"
			w.stats.MissingInline++
		}
		return w.placeholder(label, class, alt, width, height, n)
	}
}

type imageSource int

const (
	imageMissing imageSource = iota
	imageRemote
	imageInline
	imageData
)

type imageSpec struct {
	kind     imageSource
	position int
	sha      string
	url      string
	cid      bool
}

func (w *walker) resolveImage(src, srcset string) imageSpec {
	s := strings.TrimSpace(src)
	if s == "" && strings.TrimSpace(srcset) != "" {
		for _, candidate := range srcsetURLs(srcset) {
			if spec := w.resolveImage(candidate, ""); spec.kind != imageMissing {
				return spec
			}
		}
	}
	if s == "" {
		return imageSpec{}
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "data:") {
		if safeDataImage(s) {
			return imageSpec{kind: imageData, url: s}
		}
		return imageSpec{}
	}
	if strings.HasPrefix(lower, "cid:") {
		id := strings.TrimSpace(s[4:])
		id = strings.Trim(id, "<>")
		if decoded, err := url.PathUnescape(id); err == nil {
			id = decoded
		}
		key := strings.ToLower(id)
		if pos, ok := w.inline[key]; ok {
			return imageSpec{kind: imageInline, position: pos, sha: w.attachmentSHA(pos)}
		}
		return imageSpec{cid: w.knownCID[key] || key != ""}
	}
	u, err := url.Parse(s)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https" || (u.Scheme == "" && u.Host != "")) {
		return imageSpec{kind: imageRemote}
	}
	key := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(s, "./"), "/"))
	if pos, ok := w.locations[key]; ok {
		return imageSpec{kind: imageInline, position: pos, sha: w.attachmentSHA(pos)}
	}
	if slash := strings.LastIndexByte(key, '/'); slash >= 0 {
		if pos, ok := w.locations[key[slash+1:]]; ok {
			return imageSpec{kind: imageInline, position: pos, sha: w.attachmentSHA(pos)}
		}
	}
	return imageSpec{}
}

func (w *walker) attachmentSHA(position int) string {
	// The index is the attachment position; the SHA is read from the one
	// indexed list. The renderer never keeps bytes.
	if position < 0 || position >= len(w.shas) {
		return ""
	}
	return w.shas[position]
}

func (w *walker) placeholder(label, class, alt, width, height string, n *xhtml.Node) error {
	if alt != "" {
		label += ": " + alt
	}
	classes := "mail-image-placeholder " + class
	if box := w.css.box(width, height); box != "" {
		classes += " " + box
	}
	attrs := ` class="` + classes + `" role="img" aria-label="` + stdhtml.EscapeString(label) + `"`
	if align := safeAlign(nodeAttr(n, "align"), false); align != "" {
		attrs += ` data-image-align="` + align + `"`
	}
	return w.write(`<span` + attrs + w.styleAttr(n) + `>` + stdhtml.EscapeString(label) + `</span>`)
}

func (w *walker) attrs(n *xhtml.Node, tag string) string {
	allowed := elementAttributes[tag]
	var b strings.Builder
	if tag == "table" {
		b.WriteString(` data-mail-table="` + tableKind(n) + `"`)
	}
	if tag == "center" {
		b.WriteString(` data-center="1"`)
	}
	seen := map[string]bool{}
	if allowed != nil {
		for _, a := range n.Attr {
			key := strings.ToLower(a.Key)
			if !allowed[key] || seen[key] {
				continue
			}
			switch key {
			case "colspan", "rowspan", "span":
				if v, ok := boundedInt(a.Val, 1, 1000); ok {
					b.WriteString(` ` + key + `="` + v + `"`)
					seen[key] = true
				}
			case "start":
				if v, ok := boundedInt(a.Val, 1, 1000000); ok {
					b.WriteString(` start="` + v + `"`)
					seen[key] = true
				}
			case "scope":
				switch strings.ToLower(strings.TrimSpace(a.Val)) {
				case "row", "col", "rowgroup", "colgroup":
					b.WriteString(` scope="` + strings.ToLower(strings.TrimSpace(a.Val)) + `"`)
					seen[key] = true
				}
			case "width", "height":
				if v := dimension(a.Val); v != "" {
					b.WriteString(` ` + key + `="` + strings.TrimSuffix(v, "px") + `"`)
					seen[key] = true
				}
			case "align":
				if v := safeAlign(a.Val, false); v != "" {
					b.WriteString(` align="` + v + `"`)
					seen[key] = true
				}
			case "valign":
				if v := safeAlign(a.Val, true); v != "" {
					b.WriteString(` valign="` + v + `"`)
					seen[key] = true
				}
			case "bgcolor", "color":
				if safeColor(a.Val) {
					b.WriteString(` ` + key + `="` + stdhtml.EscapeString(a.Val) + `"`)
					seen[key] = true
				}
			case "face":
				if safeFont(a.Val) {
					b.WriteString(` face="` + stdhtml.EscapeString(a.Val) + `"`)
					seen[key] = true
				}
			case "size":
				if v, ok := boundedInt(a.Val, 1, 7); ok {
					b.WriteString(` size="` + v + `"`)
					seen[key] = true
				}
			case "border", "cellpadding", "cellspacing":
				if v, ok := boundedInt(a.Val, 0, 100); ok {
					b.WriteString(` ` + key + `="` + v + `"`)
					seen[key] = true
				}
			case "nowrap":
				b.WriteString(` nowrap`)
				seen[key] = true
			}
		}
	}
	b.WriteString(w.globalAttrs(n))
	b.WriteString(w.styleAttr(n))
	return b.String()
}

// globalAttrs emits dir, lang, title, class, id and role. class and id are
// validation-only selectors: the compiled sheet is the only CSS that can use
// them, and the iframe contains nothing but this message.
func (w *walker) globalAttrs(n *xhtml.Node) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		if !globalAttributes[key] || seen[key] {
			continue
		}
		seen[key] = true
		switch key {
		case "dir":
			if v, ok := safeDir(a.Val); ok {
				b.WriteString(` dir="` + v + `"`)
			}
		case "lang":
			if v, ok := safeLang(a.Val); ok {
				b.WriteString(` lang="` + stdhtml.EscapeString(v) + `"`)
			}
		case "title":
			b.WriteString(` title="` + stdhtml.EscapeString(truncateRunes(a.Val, 512)) + `"`)
		case "class":
			if v := safeClassList(a.Val); v != "" {
				b.WriteString(` class="` + stdhtml.EscapeString(v) + `"`)
			}
		case "id":
			if v := safeFragmentID(a.Val); v != "" {
				b.WriteString(` id="` + stdhtml.EscapeString(v) + `"`)
			}
		case "role":
			if v := safeRole(a.Val); v != "" {
				b.WriteString(` role="` + v + `"`)
			}
		}
	}
	return b.String()
}

func (w *walker) styleAttr(n *xhtml.Node) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "style") {
			return w.css.inline(a.Val)
		}
	}
	return ""
}

func (w *walker) collectStyles(n *xhtml.Node) {
	if n.Type == xhtml.ElementNode && strings.EqualFold(n.Data, "style") {
		var body strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == xhtml.TextNode {
				body.WriteString(c.Data)
			}
		}
		w.css.raw(sanitizeStylesheet(body.String()))
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.collectStyles(c)
	}
}

func (w *walker) bodyStyle(body *xhtml.Node) {
	decls := ""
	for _, a := range body.Attr {
		switch strings.ToLower(a.Key) {
		case "style":
			decls = sanitizeDeclarations(a.Val)
		case "bgcolor":
			if safeColor(a.Val) {
				decls += "background-color:" + stdhtml.EscapeString(a.Val) + ";"
			}
		}
	}
	if decls != "" {
		w.css.rule("body", decls)
	}
}

func tableKind(n *xhtml.Node) string {
	role := strings.ToLower(strings.TrimSpace(nodeAttr(n, "role")))
	if role == "presentation" || role == "none" {
		return "layout"
	}
	if border := strings.TrimSpace(nodeAttr(n, "border")); border != "" && border != "0" {
		return "data"
	}
	if hasTH(n) {
		return "data"
	}
	return "layout"
}

func hasTH(n *xhtml.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != xhtml.ElementNode {
			continue
		}
		tag := strings.ToLower(c.Data)
		if tag == "th" {
			return true
		}
		if tag == "table" {
			continue
		}
		if hasTH(c) {
			return true
		}
	}
	return false
}

func nodeAttr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func hidden(n *xhtml.Node) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "hidden") {
			return true
		}
		if strings.EqualFold(a.Key, "style") {
			s := strings.ToLower(strings.ReplaceAll(a.Val, " ", ""))
			if strings.Contains(s, "display:none") {
				return true
			}
		}
	}
	return false
}

// dimension normalises a width/height attribute to a bounded CSS length. A
// bare number is pixels; a percentage stays a percentage. Anything else is
// dropped rather than guessed.
func dimension(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if strings.HasSuffix(s, "%") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "%"))
		if err == nil && n > 0 && n <= 100 {
			return strconv.Itoa(n) + "%"
		}
		return ""
	}
	s = strings.TrimSuffix(s, "px")
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 2000 {
		return ""
	}
	return strconv.Itoa(n) + "px"
}

func safeAlign(raw string, vertical bool) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if vertical {
		switch v {
		case "top", "middle", "bottom", "baseline":
			return v
		}
		return ""
	}
	switch v {
	case "left", "center", "right", "justify":
		return v
	}
	return ""
}

func safeClassList(raw string) string {
	fields := strings.Fields(raw)
	out := make([]string, 0, min(len(fields), 8))
	for _, f := range fields {
		if len(f) == 0 || len(f) > 64 {
			continue
		}
		ok := true
		for _, r := range f {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, f)
		}
		if len(out) == 8 {
			break
		}
	}
	return strings.Join(out, " ")
}

func safeFragmentID(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 64 {
		return ""
	}
	for i, r := range s {
		if i == 0 {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return ""
			}
			continue
		}
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	return s
}

func safeRole(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || len(s) > 32 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	return s
}

func safeDataImage(src string) bool {
	if len(src) > maxDataImageBytes {
		return false
	}
	lower := strings.ToLower(src)
	for _, prefix := range []string{
		"data:image/png;base64,", "data:image/jpeg;base64,", "data:image/gif;base64,", "data:image/webp;base64,",
	} {
		if strings.HasPrefix(lower, prefix) {
			return len(src) > len(prefix)
		}
	}
	return false
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
