package layer1

import "fmt"

// The decider block: the configured triage decider's stored answer for every
// question, as a sparse feature. The decider is a second, independent model
// whose answers are available at serve time; its top class and confidence
// are the strongest cheap signal a small local head can stack. The block is
// appended after the lexical features, one 15-column group per question:
// 0..10 the top class, 11..13 the confidence bucket, 14 "no answer".
const deciderCellsPerQuestion = 15

// extendDesignWithDecider appends the decider's answers to a built design.
// It adds nothing when the run has no decider answers. The layout id gains
// the block version so old warm-start weights are never reused.
func extendDesignWithDecider(plan *Plan, g *gathered, layout *Layout, d *Design) (bool, error) {
	if g == nil || len(g.ondelle) == 0 || len(plan.Profile.Questions) == 0 {
		return false, nil
	}
	extra := len(plan.Profile.Questions) * deciderCellsPerQuestion
	base := d.Cells
	if err := d.ExtendSparse(extra, func(row int, _ int64, dst []int32) []int32 {
		for qi := range plan.Profile.Questions {
			q := &plan.Profile.Questions[qi]
			group := base + qi*deciderCellsPerQuestion
			c := g.ondelle[q.Name]
			if c == nil {
				dst = append(dst, int32(group+14))
				continue
			}
			top, confidence, ok := c.top(row)
			if !ok {
				dst = append(dst, int32(group+14))
				continue
			}
			dst = append(dst, int32(group+top))
			bucket := 0
			switch {
			case confidence >= 0.8:
				bucket = 2
			case confidence >= 0.6:
				bucket = 1
			}
			dst = append(dst, int32(group+11+bucket))
		}
		return dst
	}); err != nil {
		return false, fmt.Errorf("layer1: decider features: %w", err)
	}
	layout.ID += "+decider-" + featureVersion
	return true, nil
}
