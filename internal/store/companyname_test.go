package store

import "testing"

func TestDomainDisplayNameSegmentsLocalBusinessVocabulary(t *testing.T) {
	cases := []struct {
		domain string
		people map[string]int
		want   string
	}{
		{"messagerie-exemple.test", nil, "Messagerie Exemple"},
		{"climatisation-exemple.test", nil, "Climatisation Exemple"},
		{"climatisation-exemple.test", map[string]int{"Robert Mercier": 3}, "Climatisation Exemple"},
		{"wapiti-construction.test", map[string]int{"Yves Lemaire": 2}, "Wapiti Construction"},
		{"entreprises-dupont.test", map[string]int{"Marc Dupont": 2}, "Entreprises Dupont"},
		{"plomberie-marcdupont.test", map[string]int{"Marc Dupont": 2}, "Plomberie Marc Dupont"},
		{"services-exemple.test", nil, "Services Exemple"},
		{"clinique-exemple.test", nil, "Clinique Exemple"},
		{"ouest-immobilier.test", nil, "Ouest Immobilier"},
		{"horizon-recherche.test", nil, "Horizon Recherche"},
		{"club-elan.test", nil, "Club Élan"},
		{"batiment-exemple.test", nil, "Bâtiment Exemple"},
		{"lumen-studio.test", nil, "Lumen Studio"},
		{"jade-industrie.test", nil, "Jade Industrie"},
		{"famille-exemple.test", nil, "Famille Exemple"},
		{"fjord-energie.test", nil, "Fjord Énergie"},
		{"echo-logistique.test", nil, "Écho Logistique"},
		{"tundra-voyages.test", nil, "Tundra Voyages"},
		{"nova-distribution.test", nil, "Nova Distribution"},
		{"systemes-meridien.test", nil, "Systèmes Méridien"},
		{"meridien-groupe.test", nil, "Méridien Groupe"},
		{"rive-education.test", nil, "Rive Éducation"},
		{"prisme-architecture.test", nil, "Prisme Architecture"},
		{"zenith-conseil.test", nil, "Zénith Conseil"},
		{"boreal-conseil.test", nil, "Boréal Conseil"},
		{"fondation-aurore.test", nil, "Fondation Aurore"},
		{"vantage-plein-air.test", nil, "Vantage Plein Air"},
	}
	for _, test := range cases {
		if got := domainDisplayName(test.domain, test.people); got != test.want {
			t.Errorf("domainDisplayName(%q) = %q, want %q", test.domain, got, test.want)
		}
	}
}

func TestDomainDisplayNameNeverReturnsTheRawDomain(t *testing.T) {
	for _, domain := range []string{"atelier-nord.test", "www.example.com", "delta.co"} {
		got := domainDisplayName(domain, nil)
		if got == "" || got == domain {
			t.Errorf("domainDisplayName(%q) = %q", domain, got)
		}
	}
}
