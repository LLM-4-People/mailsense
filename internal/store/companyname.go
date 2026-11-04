package store

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// companyWords is the local vocabulary used to segment a domain label into a
// readable company name. It is not a company directory: it only restores the
// spaces and accents a domain cannot carry. Unknown brands stay one word.
var companyWords = map[string]string{
	"affaires": "Affaires", "agence": "Agence", "american": "American",
	"architecte": "Architecte", "artisan": "Artisan", "avocat": "Avocat",
	"batiment": "Bâtiment", "beauport": "Beauport", "boost": "Boost",
	"boreal": "Boréal", "brossard": "Brossard",
	"bureau": "Bureau", "cabinets": "Cabinets", "canada": "Canada",
	"centre": "Centre", "chantier": "Chantier", "chevron": "Chevron",
	"chicoutimi": "Chicoutimi", "climatisation": "Climatisation",
	"clinique": "Clinique", "compagnie": "Compagnie", "comptable": "Comptable",
	"construction": "Construction", "constructions": "Constructions",
	"conseil": "Conseil", "conseillers": "Conseillers", "controle": "Contrôle",
	"distribution":  "Distribution",
	"drummondville": "Drummondville", "echo": "Écho", "education": "Éducation",
	"electricite": "Électricité",
	"electrique":  "Électrique", "electronique": "Électronique",
	"elan": "Élan", "emballages": "Emballages", "energie": "Énergie",
	"entreprise": "Entreprise", "entreprises": "Entreprises",
	"equipement": "Équipement", "equipements": "Équipements",
	"est": "Est", "firme": "Firme", "gatineau": "Gatineau", "gestion": "Gestion",
	"group": "Group", "groupe": "Groupe",
	"immobilier": "Immobilier", "industrie": "Industrie", "industries": "Industries",
	"inf": "INF", "integration": "Intégration", "jonquiere": "Jonquière",
	"laval": "Laval", "levis": "Lévis", "logement": "Logement",
	"logistique": "Logistique", "longueuil": "Longueuil",
	"manufacturier": "Manufacturier", "marketing": "Marketing",
	"mecanique": "Mécanique", "media": "Média", "medical": "Médical",
	"meridien": "Méridien", "montreal": "Montréal",
	"nord": "Nord", "north": "North", "ouest": "Ouest",
	"plomberie": "Plomberie", "polymere": "Polymère",
	"production": "Production", "promo": "Promo", "propriete": "Propriété",
	"quebec":    "Québec",
	"recherche": "Recherche", "reseau": "Réseau", "restaurant": "Restaurant",
	"rimouski": "Rimouski", "rivieres": "Rivières",
	"sante": "Santé", "sept": "Sept", "services": "Services", "service": "Service",
	"sherbrooke": "Sherbrooke", "solution": "Solution", "solutions": "Solutions",
	"sud": "Sud", "systemes": "Systèmes", "systems": "Systems",
	"techno": "Techno", "technologies": "Technologies", "technology": "Technology",
	"transport": "Transport", "trois": "Trois", "victoriaville": "Victoriaville",
	"ventes": "Ventes", "zenith": "Zénith",
}

// companyAcronyms are the short tokens a domain writes without dots. They
// are displayed in capitals; everything else is title-cased.
var companyAcronyms = map[string]bool{
	"ci": true, "cpa": true, "dg": true, "grp": true,
	"inf": true, "pme": true,
}

// companyNameSuffix is the legal suffix shown after the name (Inc, Ltée…).
var companyNameSuffix = map[string]string{
	"inc": "Inc", "ltd": "Ltd", "llc": "LLC", "ltee": "Ltée", "sarl": "SARL",
	"sas": "SAS", "senc": "SENC", "ulc": "ULC", "eurl": "EURL", "sa": "SA",
	"srl": "SRL", "spa": "SpA",
}

// domainDisplayName turns a bare domain organization into a readable local
// label. It first tries to place a person name the organization already
// shows inside the domain (marcdupontplomberie -> Plomberie Dupont),
// then segments the remaining token with the local business
// vocabulary. It never looks up the web and never invents a word: an
// unrecognized brand stays exactly the domain label, title-cased.
func domainDisplayName(domain string, people map[string]int) string {
	base := companyDomainBase(domain)
	if base == "" {
		return strings.TrimSpace(domain)
	}
	if name, prefix, suffix, ok := personInsideDomain(base, people); ok {
		left := joinCompanyWords(prefix)
		right := joinCompanyWords(suffix)
		parts := []string{}
		if left != "" {
			parts = append(parts, left)
		}
		parts = append(parts, name)
		if right != "" {
			parts = append(parts, right)
		}
		return strings.Join(parts, " ")
	}
	if out := joinCompanyWords(base); out != "" {
		return out
	}
	return strings.TrimSpace(domain)
}

func companyDomainBase(domain string) string {
	value := strings.ToLower(strings.TrimSpace(domain))
	if index := strings.LastIndexByte(value, '@'); index >= 0 {
		value = value[index+1:]
	}
	value = strings.TrimPrefix(value, "www.")
	if index := strings.IndexByte(value, '.'); index > 0 {
		value = value[:index]
	}
	value = strings.ReplaceAll(value, "-", " ")
	value = strings.ReplaceAll(value, "_", " ")
	return strings.TrimSpace(value)
}

// personInsideDomain finds a full observed person name inside the compact
// domain label. Only names of two words or more qualify, so a brand that
// happens to contain a surname is not split.
func personInsideDomain(base string, people map[string]int) (name, prefix, suffix string, ok bool) {
	if len(people) == 0 {
		return "", "", "", false
	}
	compactBase, positions := compactWithPositions(base)
	if compactBase == "" {
		return "", "", "", false
	}
	runes := []rune(base)
	bestName := ""
	bestCount := -1
	bestStart, bestEnd := 0, 0
	for candidate, count := range people {
		candidate = strings.TrimSpace(candidate)
		if !contacts.IsPersonName(candidate) || len(strings.Fields(candidate)) < 2 {
			continue
		}
		compactName := mail.CompactFold(candidate)
		if utf8.RuneCountInString(compactName) < 5 {
			continue
		}
		index := strings.Index(compactBase, compactName)
		if index < 0 {
			continue
		}
		end := index + len(compactName)
		startRune := positions[index]
		endRune := positions[end-1] + 1
		if endRune <= startRune || endRune > len(runes) {
			continue
		}
		if count > bestCount || (count == bestCount && len([]rune(candidate)) > len([]rune(bestName))) {
			bestName, bestCount = candidate, count
			bestStart, bestEnd = startRune, endRune
		}
	}
	if bestName == "" {
		return "", "", "", false
	}
	return bestName,
		strings.TrimSpace(string(runes[:bestStart])),
		strings.TrimSpace(string(runes[bestEnd:])),
		true
}

// compactWithPositions returns the letters/digits of the folded value and,
// for every compact rune, the original rune index it came from. Domain labels
// are ASCII in practice, so one folded rune maps to one original rune.
func compactWithPositions(value string) (string, []int) {
	var b strings.Builder
	positions := []int{}
	for index, r := range []rune(value) {
		folded := []rune(mail.Fold(string(r)))
		for _, f := range folded {
			if unicode.IsLetter(f) || unicode.IsDigit(f) {
				b.WriteRune(f)
				positions = append(positions, index)
			}
		}
	}
	return b.String(), positions
}

// joinCompanyWords splits a token at the longest known word boundaries and
// restores the display spelling of each part. Separators already in the
// domain remain word boundaries.
func joinCompanyWords(value string) string {
	fields := strings.Fields(value)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if word := segmentCompanyToken(field); word != "" {
			out = append(out, word)
		}
	}
	return strings.Join(out, " ")
}

func segmentCompanyToken(token string) string {
	runes := []rune(strings.TrimSpace(token))
	if len(runes) == 0 {
		return ""
	}
	key := mail.Fold(string(runes))
	if word, ok := companyWords[key]; ok {
		return word
	}
	if suffix, ok := companyNameSuffix[key]; ok {
		return suffix
	}
	if companyAcronyms[key] {
		return strings.ToUpper(key)
	}
	parts := []string{}
	for i := 0; i < len(runes); {
		word, size := companyWordAt(runes, i)
		if size > 0 {
			// A known prefix followed by a very short unknown remainder is
			// more likely a brand or a place name than two words
			// ("fjord-energie", "estrie").
			if len(runes)-(i+size) > 0 && len(runes)-(i+size) < 4 {
				if _, nextSize := companyWordAt(runes, i+size); nextSize == 0 {
					if i == 0 {
						return titleWord(string(runes))
					}
					parts = append(parts, titleWord(string(runes[i:])))
					break
				}
			}
			parts = append(parts, word)
			i += size
			continue
		}
		// No known word starts here. If a long unknown prefix is followed by
		// a known word ("chevron" + "royal"), keep the prefix whole and let
		// the known tail start a new word. A short prefix is left attached,
		// so "tundra-voyages" is not split into "Pro Pieux".
		if i == 0 {
			if next, size := nextCompanyWord(runes, 4); size > 0 {
				parts = append(parts, titleWord(string(runes[:next])))
				i = next
				continue
			}
		}
		parts = append(parts, titleWord(string(runes[i:])))
		break
	}
	return strings.Join(parts, " ")
}

func companyWordAt(runes []rune, start int) (string, int) {
	for end := len(runes); end-start >= 2; end-- {
		key := mail.Fold(string(runes[start:end]))
		if end-start >= 3 {
			if word, ok := companyWords[key]; ok {
				return word, end - start
			}
			if suffix, ok := companyNameSuffix[key]; ok {
				return suffix, end - start
			}
		}
		if companyAcronyms[key] {
			return strings.ToUpper(key), end - start
		}
	}
	return "", 0
}

// nextCompanyWord finds the earliest known word that starts at or after
// minStart and returns its index and length.
func nextCompanyWord(runes []rune, minStart int) (int, int) {
	for start := minStart; start < len(runes); start++ {
		if _, size := companyWordAt(runes, start); size > 0 {
			return start, size
		}
	}
	return 0, 0
}

func titleWord(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])
	for i := 1; i < len(runes); i++ {
		runes[i] = unicode.ToLower(runes[i])
	}
	return string(runes)
}
