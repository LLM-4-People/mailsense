package textx

import "testing"

func TestRepair(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		n    int
	}{
		{"question", "RÃ©sume le dernier Ã©change avec messagerie-exemple.test", "Résume le dernier échange avec messagerie-exemple.test", 2},
		{"apostrophe", "lâ€™infrastructure", "l’infrastructure", 1},
		{"quote", "â€œdÃ©jÃ\u00a0 vuâ€”", "“déjà vu—", 4},
		{"accented letter", "Ã‰Ã€Ã‡", "ÉÀÇ", 2},
		{"nbsp", "25\u00c2\u00a0€", "25\u00a0€", 1},
		{"em dash kept", "Peux-tu me rÃ©sumer — Nadia ?", "Peux-tu me résumer — Nadia ?", 1},
		{"already clean", "Résume le dernier échange avec messagerie-exemple.test", "Résume le dernier échange avec messagerie-exemple.test", 0},
		{"plain accented", "café, déjà, où", "café, déjà, où", 0},
		{"ascii", "summarize the last exchange", "summarize the last exchange", 0},
		{"empty", "", "", 0},
		{"double encoded", "RÃƒÂ©sume", "Résume", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := Repair(tc.in)
			if got != tc.want || n != tc.n {
				t.Fatalf("Repair(%q) = %q, %d; want %q, %d", tc.in, got, n, tc.want, tc.n)
			}
			if again, n := Repair(got); again != got || n != 0 {
				t.Fatalf("Repair is not idempotent: %q -> %q, %d", got, again, n)
			}
		})
	}
}

func TestRepairLeavesInvalidUTF8Alone(t *testing.T) {
	in := string([]byte{'R', 0xE9, 's', 'u', 'm', 'e'})
	if got, n := Repair(in); got != in || n != 0 {
		t.Fatalf("Repair(%q) = %q, %d; want it unchanged", in, got, n)
	}
}
