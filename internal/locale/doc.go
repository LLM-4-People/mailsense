// Package locale is Mailsense's shipped language data and the pure
// functions over it, all generated from pinned public sources
// (scripts/update-locale-data.sh; pin.json; params.json holds the
// generator's choices) and never hand-written:
//
//   - Language: statistical language identification — character-trigram
//     profiles trained on each language's Universal Declaration of Human
//     Rights translations (UDHR in XML), for the CLDR locales of the
//     chosen coverage levels; only languages written in the text's
//     dominant script compete;
//   - Locale/Dates/Times: the CLDR Gregorian month, weekday and day-period
//     names and the date and time patterns of each language (plus CLDR
//     root's numeric ones, ISO 8601 order, in every language), compiled
//     into matchers; dates found are resolved against a reference date in
//     code;
//   - Amounts: ISO 4217 codes and CLDR currency symbols next to a number,
//     read with the language's decimal and grouping separators;
//   - Skeleton: the Unicode Technical Standard #39 skeleton (confusables),
//     the comparison form of look-alike strings.
//
// A broken data file is a build defect (the tests load every file).
package locale
