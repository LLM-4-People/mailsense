package evidence

import "github.com/LLM-4-People/mailsense/internal/locale"

func init() {
	register(Signal{Data: textData(), Name: "language", Version: 2, Type: TypeEnum, Values: append(locale.Languages(), locale.Undetermined),
		Causal: true, Group: GroupContent, Settings: []string{settingMinLetters, settingLangChars},
		Doc:     "The language of the own text (locale.Language: n-gram likelihood against the shipped profiles of the languages of its scripts; Hiragana and Katakana are one writing system, so either identifies Japanese; und below evidence.language.min_letters letters).",
		Extract: func(in *Input) Value { return Str(in.Language().Tag) }})
}
