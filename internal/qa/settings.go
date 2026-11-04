package qa

// Settings the package owns. A key spelled at its read sites drifts: a typo
// silently reads the schema default instead of the configured value, so every
// read goes through one of these names and the schema check in
// settings_test.go fails when one stops resolving. search.max_hits is owned by
// the store (store.SettingSearchMaxHits); the retrieval cap is read through
// that name, not a second constant here.
const (
	// SettingEnabled gates the whole service.
	SettingEnabled = "qa.enabled"
	// SettingProvider names the encoder/generator record of the planner.
	SettingProvider = "qa.provider"
	// SettingMaxAnswerTokens caps one generated answer.
	SettingMaxAnswerTokens = "qa.max_answer_tokens"
	// SettingAnswerLanguageMinLetters is the shortest answer the language
	// check judges.
	SettingAnswerLanguageMinLetters = "qa.answer_language.min_letters"
	// SettingMaxMessages caps the retrieval context of one answer.
	SettingMaxMessages = "qa.max_messages"
	// SettingBudgetMaxCalls and SettingBudgetMaxUSD bound the paid calls of
	// one question.
	SettingBudgetMaxCalls = "qa.budget.max_calls"
	SettingBudgetMaxUSD   = "qa.budget.max_usd"
	// SettingCallTimeout bounds one provider call, in seconds.
	SettingCallTimeout = "qa.call_timeout"
	// SettingRetrievalTimeout bounds the local retrieval of one question.
	SettingRetrievalTimeout = "qa.retrieval_timeout"
)
