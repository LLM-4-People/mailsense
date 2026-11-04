package qa

import (
	"github.com/LLM-4-People/mailsense/internal/locale"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// finalizeGeneratedAnswer is the one post-grounding language check. A
// written answer is returned exactly as the provider wrote it: the check
// never retries, rewrites, translates or falls back to retrieval. When the
// answer's own text (quoted reply lines stripped) is confidently in another
// language than the one the owner requested, the view carries
// NoticeAnswerLanguage so the UI can show the answer beside a warning. A
// short or ambiguous answer stays unjudged. A view that already carries a
// notice keeps it: the language notice is only for a clean generated view,
// so it can never overwrite the cause of a fallback.
func (s *Service) finalizeGeneratedAnswer(view AskView, language string) AskView {
	if !validLanguage(language) {
		return view
	}
	cleaned := mail.CleanBody(view.Answer)
	guess := locale.Language(cleaned, s.cfg.Int("qa.answer_language.min_letters"))
	if guess.Tag == locale.Undetermined || guess.Tag == language {
		return view
	}
	if view.Notice != "" {
		s.log.Warn("qa answer language mismatch suppressed",
			"requested", language, "detected", guess.Tag,
			"provider", view.Provider, "model", view.Model, "notice", view.Notice)
		return view
	}
	s.log.Warn("qa answer language notice", "notice", NoticeAnswerLanguage,
		"requested", language, "detected", guess.Tag,
		"provider", view.Provider, "model", view.Model)
	view.Notice = NoticeAnswerLanguage
	return view
}
