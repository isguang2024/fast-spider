//go:build !windows && !linux

package server

import (
	"errors"
	"os"
)

type releaseFileIdentity struct{}

func releaseFileIdentityForFile(_ *os.File, _ os.FileInfo) (releaseFileIdentity, error) {
	return releaseFileIdentity{}, errors.New("release file identity is unsupported on this platform")
}
