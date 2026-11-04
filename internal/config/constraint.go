package config

import (
	"fmt"
	"time"
)

// Constraint relates two settings of the same numeric type: Key Op Other,
// with Op "<" or "<=". It is checked on the effective values.
type Constraint struct {
	Key   string `json:"key"`
	Op    string `json:"op"`
	Other string `json:"other"`
}

func (s *Schema) compileConstraints(cs []Constraint, problems *Problems) []Constraint {
	var out []Constraint
	for _, c := range cs {
		a, okA := s.byKey[c.Key]
		b, okB := s.byKey[c.Other]
		switch {
		case !okA || !okB:
			problems.add(c.Key, "schema", fmt.Sprintf("constraint refers to an unknown setting (%s %s %s)", c.Key, c.Op, c.Other))
		case c.Op != "<" && c.Op != "<=":
			problems.add(c.Key, "schema", fmt.Sprintf("constraint operator %q must be < or <=", c.Op))
		case a.Type != b.Type || (a.Type != TypeInt && a.Type != TypeFloat && a.Type != TypeDuration):
			problems.add(c.Key, "schema", "constraint needs two int, float or duration settings of the same type")
		default:
			out = append(out, c)
		}
	}
	return out
}

// check returns the constraints the snapshot violates, as problems.
func (s *Schema) checkConstraints(snap *Snapshot) Problems {
	var problems Problems
	for _, c := range s.constraints {
		a, b := snap.values[c.Key], snap.values[c.Other]
		if !holds(a, b, c.Op) {
			problems.add(c.Key, "constraint", fmt.Sprintf("must be %s %s (%v), got %v", c.Op, c.Other, encodeValue(b), encodeValue(a)))
		}
	}
	return problems
}

func holds(a, b any, op string) bool {
	var x, y float64
	switch av := a.(type) {
	case int64:
		x, y = float64(av), float64(b.(int64))
	case float64:
		x, y = av, b.(float64)
	case time.Duration:
		x, y = float64(av), float64(b.(time.Duration))
	}
	if op == "<" {
		return x < y
	}
	return x <= y
}
