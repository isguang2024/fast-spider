//go:build !windows

package node

func maybeEnsureWSLKeepAlive([]string) error { return nil }
