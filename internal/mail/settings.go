package mail

// Setting keys owned by the mail domain. Threading, evidence, the installer's
// learner and the store's company book all read the same subject and shared
// domain settings; a second spelling in any of them was a drift waiting to
// happen.
const (
	SettingSubjectPrefixes        = "mail.subject.prefixes"
	SettingSubjectPrefixesLearned = "mail.subject.prefixes_learned"
	SettingSharedDomains          = "mail.domains.shared"
)
