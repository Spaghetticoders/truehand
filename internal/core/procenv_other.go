//go:build !darwin && !linux

package core

func processEnv(int) ([]string, bool) { return nil, false }
