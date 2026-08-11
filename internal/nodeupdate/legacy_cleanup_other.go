//go:build !windows

package nodeupdate

// CleanupLegacyInstallArtifacts is a no-op outside Windows because the legacy
// artifacts only existed in the Windows single-EXE installation layout.
func CleanupLegacyInstallArtifacts(string) error { return nil }
