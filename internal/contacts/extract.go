package contacts

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// SignatureEvidence is the local, display-only evidence one signature block
// carries for its sender. Every field is optional; nothing here is invented
// from the body outside the signature CleanBody already located.
type SignatureEvidence struct {
	Name    string
	Phone   string
	Title   string
	Company string
}

// stopNameWords are role, greeting, department and company words that make a
// capitalized line a signature label rather than a person's name. They are
// folded before lookup, so accents and case do not matter.
var extraStopNameWords = map[string]bool{
	"merci": true, "bonjour": true, "salut": true, "cordialement": true,
	"salutations": true, "bienvenue": true, "hello": true, "thanks": true,
	"thank": true, "regards": true, "sincerely": true, "confiance": true,
	"rep": true, "sales": true, "support": true,
	"service": true, "services": true, "administration": true, "comptabilite": true,
	"ressources": true, "humaines": true, "ventes": true, "marketing": true,
	"direction": true, "bureau": true, "equipe": true, "departement": true,
	"reception": true, "info": true,
	"contact": true, "reseau": true, "ti": true,
	"inc": true, "ltd": true, "ltee": true, "sarl": true, "sas": true,
	"senc": true, "corp": true, "corporation": true, "gmbh": true, "llc": true,
	"plc": true, "ulc": true, "eurl": true, "sa": true, "srl": true, "spa": true,
	"company": true, "compagnie": true, "groupe": true, "group": true,
	"technologies": true, "technology": true, "solutions": true, "systems": true,
	"systemes": true,
	"client":   true, "fournisseur": true, "expediteur": true, "destinataire": true,
	"unknown": true, "inconnu": true, "nobody": true, "noreply": true,
	"notifications": true, "notification": true, "alertes": true, "alerte": true,
	"admin": true, "administrator": true, "webmaster": true, "postmaster": true,
	"billing": true, "facturation": true, "commandes": true, "orders": true,
	"shipping": true, "livraison": true,
	"monsieur": true, "madame": true, "mme": true, "mr": true, "mrs": true,
	"ms": true, "dr": true, "dre": true, "me": true,
}

// titleWords are the role and department words that identify a signature
// title. A greeting or a company suffix is never a title.
var titleWords = map[string]bool{
	"directeur": true, "directrice": true, "adjoint": true, "adjointe": true,
	"consultant": true, "consultante": true, "gerant": true, "president": true,
	"presidente": true, "vp": true, "chef": true, "coordonnateur": true,
	"coordonnatrice": true, "representant": true, "technicien": true,
	"technicienne": true, "comptable": true, "ingenieur": true, "architecte": true,
	"avocat": true, "avocate": true, "notaire": true, "pharmacien": true,
	"pharmacienne": true, "medecin": true, "infirmier": true, "infirmiere": true,
	"proprietaire": true, "fondateur": true, "fondatrice": true, "associe": true,
	"partenaire": true, "conseiller": true, "conseillere": true, "analyste": true,
	"developpeur": true, "gestionnaire": true, "secretaire": true,
}

// stopNameWords is titleWords plus the non-title words: one lookup at the
// signature boundary, and a new title word needs only the title table.
var stopNameWords = func() map[string]bool {
	out := make(map[string]bool, len(titleWords)+len(extraStopNameWords))
	for w := range titleWords {
		out[w] = true
	}
	for w := range extraStopNameWords {
		out[w] = true
	}
	return out
}()

var legalSuffixes = map[string]bool{
	"inc": true, "inc.": true, "ltd": true, "ltd.": true, "ltee": true,
	"ltée": true, "sarl": true, "sas": true, "senc": true, "corp": true,
	"corp.": true, "corporation": true, "gmbh": true, "llc": true, "plc": true,
	"ulc": true, "eurl": true, "sa": true, "srl": true, "spa": true, "s.a.": true,
}

// cleanLine trims a signature line and the bullets or rules a contact block
// often puts around it.
func cleanLine(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, " \t|•*-–—")
	return strings.TrimSpace(value)
}

func signatureLines(signature string) []string {
	raw := strings.Split(strings.ReplaceAll(signature, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if line = cleanLine(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func hasLetter(value string) bool {
	return strings.IndexFunc(value, unicode.IsLetter) >= 0
}

func hasDigit(value string) bool {
	return strings.IndexFunc(value, unicode.IsDigit) >= 0
}

// personName reports whether value is a human name, not a role, greeting or
// company line. allowSingle permits a one-word display name from an address
// book; signature extraction requires at least two words.
func personName(value string, allowSingle bool) bool {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 80 {
		return false
	}
	if strings.ContainsAny(value, "@/:;()[]{}<>|+=_") || !hasLetter(value) || hasDigit(value) {
		return false
	}
	tokens := strings.Fields(value)
	if len(tokens) < 2 && !allowSingle {
		return false
	}
	if len(tokens) > 4 {
		return false
	}
	for _, token := range tokens {
		trimmed := strings.Trim(token, ".,'’-")
		if trimmed == "" {
			return false
		}
		if stopNameWords[mail.Fold(trimmed)] {
			return false
		}
		first, _ := utf8.DecodeRuneInString(trimmed)
		if !unicode.IsUpper(first) {
			return false
		}
		for _, r := range trimmed {
			if unicode.IsLetter(r) || unicode.IsMark(r) || r == '-' || r == '\'' || r == '’' || r == '.' {
				continue
			}
			return false
		}
	}
	return true
}

// IsPersonName reports whether value has the shape of a human display name.
func IsPersonName(value string) bool { return personName(value, true) }

// BestName picks the one display name for an address. A declared address-book
// name wins; otherwise the most frequent observed spelling wins, then the
// longer and finally the lexically first spelling. Generic role names never
// win over a real name and are empty when nothing else exists.
func BestName(names map[string]int, declared string) string {
	best := ""
	bestScore := -1
	for name, count := range names {
		name = strings.TrimSpace(name)
		if !IsPersonName(name) {
			continue
		}
		// A full name ("Robert Smith") beats a nickname ("Bob") at a close
		// frequency, while a clearly more frequent spelling still wins.
		score := count*1000 + len(strings.Fields(name))*1000 + utf8.RuneCountInString(name)
		if score > bestScore || (score == bestScore && name < best) {
			best, bestScore = name, score
		}
	}
	declared = strings.TrimSpace(declared)
	if IsPersonName(declared) {
		return declared
	}
	return best
}

// PhoneKey is the comparison form of a phone: its digits, with a leading +
// preserved when the source wrote one. Display formatting stays untouched.
func PhoneKey(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if digits == "" {
		return ""
	}
	if strings.HasPrefix(value, "+") {
		return "+" + digits
	}
	return digits
}

var phoneLabelRE = regexp.MustCompile(`(?i)^\s*(t[ée]l[ée]phone|t[ée]l|phone|mobile|cell(?:ulaire)?|bureau|office|sans frais|toll free|fax|t[ée]l[ée]copieur)\s*[:.\-–—]?\s*`)

func phoneFromLine(line string) string {
	lower := mail.Fold(line)
	if strings.Contains(line, "@") || strings.Contains(lower, "http") || strings.Contains(lower, "www.") {
		return ""
	}
	candidate := line
	if index := strings.IndexAny(candidate, ":："); index >= 0 && index < 28 {
		candidate = candidate[index+1:]
	}
	candidate = strings.TrimSpace(strings.TrimLeft(candidate, "-–—|"))
	candidate = strings.TrimSpace(phoneLabelRE.ReplaceAllString(candidate, ""))
	if !hasDigit(candidate) {
		return ""
	}
	match := mail.PhonePattern().FindString(candidate)
	if match == "" {
		return ""
	}
	digits := PhoneKey(match)
	if len(strings.TrimPrefix(digits, "+")) < 7 || len(strings.TrimPrefix(digits, "+")) > 20 {
		return ""
	}
	return CleanPhone(match)
}

func looksLikeTitle(line string) bool {
	if !hasLetter(line) || hasDigit(line) || strings.ContainsAny(line, "@<>") {
		return false
	}
	lower := mail.Fold(line)
	if strings.Contains(lower, "http") || strings.Contains(lower, "www.") {
		return false
	}
	tokens := strings.Fields(line)
	if len(tokens) < 2 || len(tokens) > 8 || utf8.RuneCountInString(line) > 90 {
		return false
	}
	for _, token := range tokens {
		if titleWords[mail.Fold(strings.Trim(token, ".,'’-"))] {
			return true
		}
	}
	return false
}

// ExtractSignature reads the sender's signature block. The explicit header
// name is preferred when it is a person name; the signature fills a missing
// name, phone, title and company. It never returns a quoted owner signature:
// CleanBody has already cut the quoted reply and the current signature.
func ExtractSignature(signature, headerName, address string) SignatureEvidence {
	lines := signatureLines(signature)
	out := SignatureEvidence{}
	if IsPersonName(headerName) {
		out.Name = strings.TrimSpace(headerName)
	}
	for _, line := range lines {
		if out.Phone == "" {
			out.Phone = phoneFromLine(line)
		}
		if out.Name == "" && personName(line, false) {
			out.Name = line
		}
		if out.Title == "" && looksLikeTitle(line) {
			out.Title = line
		}
	}
	out.Company = ExtractCompany(signature, address)
	return out
}

// ExtractCompany returns the strongest company line in a signature. A legal
// suffix is strong evidence; a name that overlaps the sender's domain is the
// other local signal. It never invents a company from a greeting or a bare
// person name.
func ExtractCompany(signature, address string) string {
	domain := companyDomainLabel(address)
	best := ""
	bestScore := 0
	for _, line := range signatureLines(signature) {
		if len([]rune(line)) > 100 || strings.ContainsAny(line, "@<>") {
			continue
		}
		lower := mail.Fold(line)
		if strings.Contains(lower, "http") || strings.Contains(lower, "www.") {
			continue
		}
		if phoneFromLine(line) != "" && !hasLetter(strings.ReplaceAll(line, " ", "")) {
			continue
		}
		tokens := strings.Fields(line)
		if len(tokens) == 0 || len(tokens) > 10 {
			continue
		}
		compact := mail.CompactFold(line)
		score := 0
		last := mail.Fold(strings.Trim(tokens[len(tokens)-1], ".,;:()"))
		if legalSuffixes[last] {
			score += 4
		}
		if domain != "" && len([]rune(domain)) >= 4 &&
			(strings.Contains(compact, domain) || strings.Contains(domain, compact)) {
			score += 3
		}
		if score < 3 {
			continue
		}
		if score > bestScore || (score == bestScore && len([]rune(line)) > len([]rune(best))) {
			best, bestScore = line, score
		}
	}
	return best
}

// companyDomainLabel is the first label of the registrable domain of an
// address or domain, folded for comparison. It is only a matching token, not
// a display name.
func companyDomainLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if index := strings.LastIndexByte(value, '@'); index >= 0 {
		value = value[index+1:]
	}
	value = strings.TrimPrefix(value, "www.")
	if index := strings.IndexByte(value, '.'); index > 0 {
		value = value[:index]
	}
	return mail.CompactFold(value)
}
