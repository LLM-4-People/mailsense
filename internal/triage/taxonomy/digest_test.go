package taxonomy

import "testing"

// Every profile section that can change an answer is in the digest. The
// legacy Fingerprint hashes only the questions object, so it cannot be the
// route key of a profile whose thresholds or roles changed.
func TestProfileDigestCoversEverySection(t *testing.T) {
	base, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := base.Digest()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		apply func(*Profile)
	}{
		{"name", func(p *Profile) { p.Name += "2" }},
		{"body_chars", func(p *Profile) { p.BodyChars++ }},
		{"state_template", func(p *Profile) { p.StateTemplate += " " }},
		{"questions", func(p *Profile) { p.Questions[0].Instructions += " " }},
		{"roles", func(p *Profile) {
			for k, v := range p.Roles {
				p.Roles[k] = v + "2"
				break
			}
		}},
		{"thresholds", func(p *Profile) {
			for k := range p.Thresholds {
				p.Thresholds[k] += 0.01
				break
			}
		}},
		{"prior_blend", func(p *Profile) {
			for k := range p.PriorBlend {
				p.PriorBlend[k] += 0.01
				break
			}
		}},
		{"prior_min_samples", func(p *Profile) { p.PriorMinSamples++ }},
		{"include_history", func(p *Profile) { p.IncludeHistory = !p.IncludeHistory }},
		{"include_examples", func(p *Profile) { p.IncludeExamples = !p.IncludeExamples }},
		{"include_facts", func(p *Profile) { p.IncludeFacts = !p.IncludeFacts }},
		{"max_examples", func(p *Profile) { p.MaxExamples++ }},
		{"escalation", func(p *Profile) {
			conf := 0.1
			p.Escalation = map[Role]EscalationRule{RoleCategory: {MinConfidence: &conf}}
		}},
		{"suspicious", func(p *Profile) { p.SuspiciousCategories = append(p.SuspiciousCategories, "x") }},
		{"known_relationships", func(p *Profile) { p.KnownRelationships = append(p.KnownRelationships, "x") }},
	}
	for _, tc := range cases {
		p, err := Parse(nil, "")
		if err != nil {
			t.Fatal(err)
		}
		tc.apply(p)
		got, err := p.Digest()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got == baseDigest {
			t.Errorf("%s is not part of the profile digest", tc.name)
		}
	}
}

// The overrides path is not a decision input: moving the same profile to a
// new file must not move the route key.
func TestProfileDigestIgnoresItsSourcePath(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	before, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	p.Source = "/tmp/another-profile.json"
	after, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("the digest follows the overrides path: %s != %s", before, after)
	}
}

func TestProfileDigestIsStable(t *testing.T) {
	one, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	two, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	a, err := one.Digest()
	if err != nil {
		t.Fatal(err)
	}
	b, err := two.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("two parses of the same profile disagree: %s != %s", a, b)
	}
}
