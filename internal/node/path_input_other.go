//go:build !windows

package node

func normalizeMachinePathInput(path string) string { return path }
