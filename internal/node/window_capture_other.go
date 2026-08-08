//go:build !windows

package node

import "image"

type nativeWindowInfo struct {
	Handle    uint64
	ProcessID uint32
	ClassName string
	Title     string
	Bounds    image.Rectangle
}

func listNativeWindows() ([]nativeWindowInfo, error) {
	return nil, ErrScreenshotUnavailable
}

func nativeWindowInfoForHandle(uint64) (nativeWindowInfo, error) {
	return nativeWindowInfo{}, ErrScreenshotUnavailable
}

func captureWindowPNG(uint64, string) error {
	return ErrScreenshotUnavailable
}
