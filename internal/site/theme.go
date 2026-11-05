package site

// The theme switch has one owner: this file. The head script reads the stored
// choice under ThemeStorageKey before paint, the stylesheet renders both
// palettes, and the generated masthead carries the three-choice control.
const (
	// ThemeStorageKey is the localStorage entry an explicit theme choice is
	// stored under. An absent entry means the system preference; the writer
	// and the reader both use it.
	ThemeStorageKey = "mailsense.theme"
)

// The three control choices, in render order.
const (
	themeSystem = "system"
	themeLight  = "light"
	themeDark   = "dark"
)

// themeChoice is one button of the theme control.
type themeChoice struct {
	Value string
	Label Localized
}

// themeChoices returns the ordered control, localized from the chrome labels.
// The generator and the tests both read this list, so a fourth choice cannot
// ship without a label.
func (s Site) themeChoices() []themeChoice {
	return []themeChoice{
		{themeSystem, s.Chrome.ThemeSystem},
		{themeLight, s.Chrome.ThemeLight},
		{themeDark, s.Chrome.ThemeDark},
	}
}
