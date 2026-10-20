// Package eyes captures what the running app actually shows: a screenshot plus the texts
// on screen, from an adapter per platform.
package eyes

import (
	"context"
	"fmt"
	"time"
)

// Step is one navigation action of a screen's route.
type Step struct {
	Tap   string `yaml:"tap,omitempty" json:"tap,omitempty"`     // accessibility label to tap
	Wait  string `yaml:"wait,omitempty" json:"wait,omitempty"`   // label that must appear
	Sleep string `yaml:"sleep,omitempty" json:"sleep,omitempty"` // Go duration
	Swipe string `yaml:"swipe,omitempty" json:"swipe,omitempty"` // up | down
	Text  string `yaml:"text,omitempty" json:"text,omitempty"`   // text to type into the focused field
	URL   string `yaml:"url,omitempty" json:"url,omitempty"`     // web: navigate
	Click string `yaml:"click,omitempty" json:"click,omitempty"` // web: CSS selector to click
}

type Capture struct {
	PNG   []byte
	Texts []string
	// Scale is device pixels per logical point (3 on a Pro iPhone, devicePixelRatio on web).
	Scale  float64
	Device string
}

type Adapter interface {
	Capture(ctx context.Context, route []Step) (*Capture, error)
}

func waitFor(ctx context.Context, timeout time.Duration, fn func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := fn()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
}
