//go:build windows

package nodeupdate

import "golang.org/x/sys/windows"

// CleanupLegacyInstallArtifacts removes only obsolete artifacts produced by
// pre-self-updater Windows installation scripts next to the running Node EXE.
func CleanupLegacyInstallArtifacts(executablePath string) error {
	return cleanupLegacyWindowsInstallArtifacts(executablePath, legacyPathIsReparse)
}

func legacyPathIsReparse(path string) (bool, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attributes, err := windows.GetFileAttributes(pathPtr)
	if err != nil {
		return false, err
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}
