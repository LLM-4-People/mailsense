package contacts

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func TestExtractSignatureFillsPersonAndCompany(t *testing.T) {
	signature := `--
Julie Roy
Adjointe administrative
Climatisation Exemple Inc.
Tél. : 819 555-0123
julie@climatisation-exemple.test
www.climatisation-exemple.test`
	got := ExtractSignature(signature, "Julie Roy", "julie@climatisation-exemple.test")
	if got.Name != "Julie Roy" {
		t.Errorf("name %q", got.Name)
	}
	if got.Phone != "819 555-0123" {
		t.Errorf("phone %q", got.Phone)
	}
	if got.Title != "Adjointe administrative" {
		t.Errorf("title %q", got.Title)
	}
	if got.Company != "Climatisation Exemple Inc." {
		t.Errorf("company %q", got.Company)
	}
}

func TestExtractSignatureFindsPhoneAboveASocialBlock(t *testing.T) {
	body := "Bonjour,\n\nVoici le suivi.\n\nMerci,\nJulie Roy\nAdjointe administrative\nClimatisation Exemple Inc.\nTél. : 819 555-0123\n\nSuivez-nous sur Facebook!\nwww.climatisation-exemple.test"
	got := ExtractSignature(mail.EvidenceText(body), "Julie Roy", "julie@climatisation-exemple.test")
	if got.Phone != "819 555-0123" {
		t.Fatalf("phone above social block %q", got.Phone)
	}
	if got.Title != "Adjointe administrative" {
		t.Fatalf("title above social block %q", got.Title)
	}
	if got.Company != "Climatisation Exemple Inc." {
		t.Fatalf("company above social block %q", got.Company)
	}
}

func TestExtractSignatureRejectsRoleHeaderAndUsesSignatureName(t *testing.T) {
	signature := `--
Marc Famille Exemple
Directeur des services de réadaptation
Clinique Clinique Exemple
Téléphone : +1 819 555-0199`
	got := ExtractSignature(signature, "Consultant TI Réseau", "marc@clinique-exemple.test")
	if got.Name != "Marc Famille Exemple" {
		t.Errorf("name %q", got.Name)
	}
	if got.Phone != "+1 819 555-0199" {
		t.Errorf("phone %q", got.Phone)
	}
	if got.Title != "Directeur des services de réadaptation" {
		t.Errorf("title %q", got.Title)
	}
	if got.Company != "Clinique Clinique Exemple" {
		t.Errorf("company %q", got.Company)
	}
}

func TestBestNamePrefersDeclaredAndMostFrequent(t *testing.T) {
	names := map[string]int{
		"Bob":                        4,
		"Robert Smith":               3,
		"Adjointe administrative":    9,
		"Merci pour votre confiance": 9,
	}
	if got := BestName(names, "Robert Smith"); got != "Robert Smith" {
		t.Errorf("declared name %q", got)
	}
	if got := BestName(names, ""); got != "Robert Smith" {
		t.Errorf("observed name %q", got)
	}
	if got := BestName(map[string]int{"Adjointe administrative": 9}, ""); got != "" {
		t.Errorf("generic name %q", got)
	}
	if got := BestName(map[string]int{"alice@example.com": 9}, ""); got != "" {
		t.Errorf("email name %q", got)
	}
}

func TestPhoneKeyKeepsDigitsAndDisplayStaysUntouched(t *testing.T) {
	if got := PhoneKey("+1 (819) 555-0123 poste 42"); got != "+1819555012342" {
		t.Errorf("phone key %q", got)
	}
	if got := CleanPhone(" +1 (819) 555-0123 "); got != "+1 (819) 555-0123" {
		t.Errorf("clean phone %q", got)
	}
}

func TestExtractCompanyNeedsLocalEvidence(t *testing.T) {
	if got := ExtractCompany("Merci et bonne journée", "messagerie-exemple.test"); got != "" {
		t.Errorf("greeting company %q", got)
	}
	if got := ExtractCompany("Messagerie Exemple\nTél. : 819 555-0123", "messagerie-exemple.test"); got != "Messagerie Exemple" {
		t.Errorf("domain company %q", got)
	}
	if got := ExtractCompany("Acme Industries Inc.", "messagerie-exemple.test"); got != "Acme Industries Inc." {
		t.Errorf("legal suffix company %q", got)
	}
}
