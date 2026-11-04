// Package learningtest holds test doubles of the learning gate. Production
// code never imports it: the real gate opens only when learning is done.
package learningtest

import "context"

// Open is a gate that is always open (tests of the features behind it).
type Open struct{}

// Allow lets everything through.
func (Open) Allow(context.Context) error { return nil }
