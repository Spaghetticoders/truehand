package eyes

import (
	"context"
	"errors"
	"os"
)

// File uses a screenshot taken elsewhere (Android, a device farm, a manual capture).
// It has no texts, so text checks are skipped.
type File struct {
	Path  string
	Scale float64
}

func (f *File) Capture(_ context.Context, route []Step) (*Capture, error) {
	if len(route) > 0 {
		return nil, errors.New("the file adapter cannot follow a route")
	}
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, err
	}
	return &Capture{PNG: b, Scale: f.Scale, Device: "file:" + f.Path}, nil
}
