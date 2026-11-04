package layer1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/triage"
)

// TeacherChoice is the choose_teacher action's input: one question, one of
// its existing teacher sources, and for the triage decider the catalog
// provider to use.
type TeacherChoice struct {
	Question string `json:"question"`
	Source   string `json:"source"`
	Provider string `json:"provider,omitempty"`
}

// ChooseTeacher enables a teacher source for one question. A label or rules
// source is written to layer1.teachers.file as that question's override
// (every other override in the file is kept; the merged document must load
// before the write). The triage decider is a setting instead: its provider
// is validated and triage.decider is set through config.Set. Every problem
// of the choice is reported at once, and a failed choice changes nothing.
func (r *Runner) ChooseTeacher(ctx context.Context, choice TeacherChoice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, err := NewPlan(r.cfg)
	if err != nil {
		return fmt.Errorf("layer1: choose teacher: %w", err)
	}
	var problems []string
	if choice.Question == "" {
		problems = append(problems, "a question is required")
	} else if _, ok := plan.Profile.Question(choice.Question); !ok {
		problems = append(problems, fmt.Sprintf("unknown question %q", choice.Question))
	}
	var source *Source
	for i := range plan.Teachers.Sources {
		if plan.Teachers.Sources[i].Name == choice.Source {
			source = &plan.Teachers.Sources[i]
			break
		}
	}
	switch {
	case choice.Source == "":
		problems = append(problems, "a source is required")
	case source == nil:
		problems = append(problems, fmt.Sprintf("unknown teacher source %q", choice.Source))
	}
	decider := source != nil && source.From == FromDecisions
	if decider && source.Name != SourceDecider {
		problems = append(problems, fmt.Sprintf("only the %q source follows triage.decider; %q is a stored decider key", SourceDecider, source.Name))
	}
	if decider && choice.Provider == "" {
		problems = append(problems, "the decider source needs a provider")
	}
	if !decider && choice.Provider != "" {
		problems = append(problems, "a provider applies only to the decider source")
	}
	if decider && choice.Provider != "" {
		cat, err := engine.LoadCatalog(r.cfg)
		if err != nil {
			problems = append(problems, err.Error())
		} else if p, err := cat.Select(choice.Provider, engine.KindDecider); err != nil {
			problems = append(problems, err.Error())
		} else if p.Runtime != "" {
			problems = append(problems, fmt.Sprintf("provider %q is the local runtime %s (a local model never teaches layer 1)", choice.Provider, p.Runtime))
		} else if !(p.ProbabilityTolerance > 0 && p.ProbabilityTolerance < 1) {
			problems = append(problems, fmt.Sprintf("provider %q has no usable probability_tolerance", choice.Provider))
		}
	}
	if decider && choice.Question != "" {
		if _, ok := plan.Profile.Question(choice.Question); ok &&
			!slices.ContainsFunc(plan.Teachers.For(choice.Question), func(s Source) bool { return s.Name == SourceDecider }) {
			problems = append(problems, fmt.Sprintf("the %q source is removed for question %q in %s; re-enable it there first",
				SourceDecider, choice.Question, SettingTeachersFile))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("layer1: choose teacher: %s", strings.Join(problems, "; "))
	}
	if decider {
		return r.chooseDecider(choice.Provider)
	}
	return r.writeTeacherSource(plan, choice, *source)
}

// chooseDecider sets triage.decider and confirms the plan still loads; an
// unexpected failure restores the previous effective value.
func (r *Runner) chooseDecider(provider string) error {
	old, oldLayer := r.cfg.String(triage.DeciderSetting), r.cfg.Snapshot().Layer(triage.DeciderSetting)
	if err := r.cfg.Set(triage.DeciderSetting, provider); err != nil {
		return fmt.Errorf("layer1: choose teacher: %w", err)
	}
	if _, err := NewPlan(r.cfg); err != nil {
		var restore error
		if oldLayer == config.LayerFile {
			restore = r.cfg.Set(triage.DeciderSetting, old)
		} else {
			restore = r.cfg.Reset(triage.DeciderSetting)
		}
		if restore != nil {
			return fmt.Errorf("layer1: choose teacher: %v (restoring triage.decider failed: %v)", err, restore)
		}
		return fmt.Errorf("layer1: choose teacher: %v (the previous decider was restored)", err)
	}
	return nil
}

// writeTeacherSource merges one question's source override into the
// teachers file, validates the merged document, and writes it atomically.
// A post-write plan failure restores the previous file.
func (r *Runner) writeTeacherSource(plan *Plan, choice TeacherChoice, base Source) error {
	path := r.cfg.Paths().Resolve(r.cfg.String(SettingTeachersFile))
	existing, existed, err := fsx.ReadOptional(path)
	if err != nil {
		return fmt.Errorf("layer1: choose teacher: read %s: %w", path, err)
	}
	doc := jsonx.Object{}
	if len(bytes.TrimSpace(existing)) > 0 {
		value, err := jsonx.DecodeOrdered(existing)
		if err != nil {
			return fmt.Errorf("layer1: choose teacher: %s: %w", path, err)
		}
		object, ok := value.(jsonx.Object)
		if !ok {
			return fmt.Errorf("layer1: choose teacher: %s: must be a JSON object", path)
		}
		doc = object
	}
	effective := base
	for _, s := range plan.Teachers.For(choice.Question) {
		if s.Name == choice.Source {
			effective = s
			break
		}
	}
	raw, err := json.Marshal(effective)
	if err != nil {
		return fmt.Errorf("layer1: choose teacher: source %s: %w", choice.Source, err)
	}
	value, err := jsonx.DecodeOrdered(raw)
	if err != nil {
		return fmt.Errorf("layer1: choose teacher: source %s: %w", choice.Source, err)
	}
	questions := jsonx.Object{{Key: choice.Question, Value: jsonx.Object{{Key: choice.Source, Value: value}}}}
	override := jsonx.Object{{Key: "questions", Value: questions}}
	merged, err := json.MarshalIndent(jsonx.MergeObject(doc, override, "questions", "questions.*"), "", "  ")
	if err != nil {
		return fmt.Errorf("layer1: choose teacher: %w", err)
	}
	merged = append(merged, '\n')
	candidate, err := jsonx.Overlay(teachersJSON, merged, "sources", "questions")
	if err != nil {
		return fmt.Errorf("layer1: choose teacher: %w", err)
	}
	if _, err := parseTeachers(candidate, plan.Profile.QuestionNames()); err != nil {
		return fmt.Errorf("layer1: choose teacher: the merged document does not load: %w", err)
	}
	if err := fsx.WriteFileAtomic(path, merged, fsx.FilePerm, r.cfg.Snapshot().Replace(nil)); err != nil {
		return fmt.Errorf("layer1: choose teacher: write %s: %w", path, err)
	}
	if _, err := NewPlan(r.cfg); err != nil {
		restore := os.Remove(path)
		if existed {
			restore = fsx.WriteFileAtomic(path, existing, fsx.FilePerm, r.cfg.Snapshot().Replace(nil))
		}
		if restore != nil {
			return fmt.Errorf("layer1: choose teacher: %v (restoring %s failed: %v)", err, path, restore)
		}
		return fmt.Errorf("layer1: choose teacher: %v (the previous file was restored)", err)
	}
	return nil
}
