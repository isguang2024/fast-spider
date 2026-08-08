//go:build windows

package node

import (
	"errors"
	"image"
	"image/png"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	dwmwaExtendedFrameBounds = 9
	dwmwaCloaked             = 14
	dibRGBColors             = 0
	biRGB                    = 0
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	gdi32                        = windows.NewLazySystemDLL("gdi32.dll")
	dwmapi                       = windows.NewLazySystemDLL("dwmapi.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procIsWindow                 = user32.NewProc("IsWindow")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procGetWindowDC              = user32.NewProc("GetWindowDC")
	procSetProcessDPIAwareness   = user32.NewProc("SetProcessDpiAwarenessContext")
	procReleaseDC                = user32.NewProc("ReleaseDC")
	procPrintWindow              = user32.NewProc("PrintWindow")
	procCreateCompatibleDC       = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC                 = gdi32.NewProc("DeleteDC")
	procCreateDIBSection         = gdi32.NewProc("CreateDIBSection")
	procSelectObject             = gdi32.NewProc("SelectObject")
	procDeleteObject             = gdi32.NewProc("DeleteObject")
	procDwmGetWindowAttribute    = dwmapi.NewProc("DwmGetWindowAttribute")
)

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type nativeWindowInfo struct {
	Handle    uint64
	ProcessID uint32
	ClassName string
	Title     string
	Bounds    image.Rectangle
}

func listNativeWindows() ([]nativeWindowInfo, error) {
	items := make([]nativeWindowInfo, 0, 32)
	stoppedForLimit := false
	callback := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		if len(items) >= 128 {
			stoppedForLimit = true
			return 0
		}
		info, err := nativeWindowInfoForHandle(uint64(hwnd))
		if err == nil && info.Title != "" {
			items = append(items, info)
		}
		return 1
	})
	result, _, callErr := procEnumWindows.Call(callback, 0)
	if result == 0 && !stoppedForLimit {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return nil, callErr
		}
		return nil, ErrScreenshotUnavailable
	}
	return items, nil
}

func nativeWindowInfoForHandle(handle uint64) (nativeWindowInfo, error) {
	if handle == 0 || uint64(uintptr(handle)) != handle {
		return nativeWindowInfo{}, ErrWindowTokenInvalid
	}
	hwnd := uintptr(handle)
	if ok, _, _ := procIsWindow.Call(hwnd); ok == 0 {
		return nativeWindowInfo{}, ErrWindowTokenInvalid
	}
	if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
		return nativeWindowInfo{}, ErrScreenshotUnavailable
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		return nativeWindowInfo{}, ErrScreenshotUnavailable
	}
	var cloaked uint32
	if hr, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaCloaked, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked)); hr == 0 && cloaked != 0 {
		return nativeWindowInfo{}, ErrScreenshotUnavailable
	}
	length, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if length == 0 || length > 2048 {
		return nativeWindowInfo{}, ErrScreenshotUnavailable
	}
	buffer := make([]uint16, int(length)+1)
	written, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if written == 0 {
		return nativeWindowInfo{}, ErrScreenshotUnavailable
	}
	title := strings.TrimSpace(windows.UTF16ToString(buffer))
	if title == "" {
		return nativeWindowInfo{}, ErrScreenshotUnavailable
	}
	var processID uint32
	if threadID, _, _ := procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&processID))); threadID == 0 || processID == 0 {
		return nativeWindowInfo{}, ErrScreenshotUnavailable
	}
	classBuffer := make([]uint16, 256)
	classLength, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&classBuffer[0])), uintptr(len(classBuffer)))
	if classLength == 0 {
		return nativeWindowInfo{}, ErrScreenshotUnavailable
	}
	className := windows.UTF16ToString(classBuffer[:classLength])
	bounds, err := nativeWindowBounds(hwnd)
	if err != nil || bounds.Empty() || bounds.Dx() < 16 || bounds.Dy() < 16 {
		return nativeWindowInfo{}, ErrScreenshotUnavailable
	}
	return nativeWindowInfo{Handle: handle, ProcessID: processID, ClassName: className, Title: title, Bounds: bounds}, nil
}

func nativeWindowBounds(hwnd uintptr) (image.Rectangle, error) {
	var rect winRect
	if hr, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaExtendedFrameBounds, uintptr(unsafe.Pointer(&rect)), unsafe.Sizeof(rect)); hr != 0 {
		if ok, _, callErr := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect))); ok == 0 {
			if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
				return image.Rectangle{}, callErr
			}
			return image.Rectangle{}, ErrScreenshotUnavailable
		}
	}
	return image.Rect(int(rect.Left), int(rect.Top), int(rect.Right), int(rect.Bottom)), nil
}

func captureWindowPNG(handle uint64, outputPath string) error {
	if err := procSetProcessDPIAwareness.Find(); err == nil {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 == -4.
		_, _, _ = procSetProcessDPIAwareness.Call(^uintptr(3))
	}
	info, err := nativeWindowInfoForHandle(handle)
	if err != nil {
		return err
	}
	width, height := info.Bounds.Dx(), info.Bounds.Dy()
	pixels := int64(width) * int64(height)
	if pixels <= 0 || pixels > maxDesktopScreenshotPixels {
		return ErrScreenshotTooLarge
	}
	hwnd := uintptr(handle)
	windowDC, _, callErr := procGetWindowDC.Call(hwnd)
	if windowDC == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return callErr
		}
		return ErrScreenshotUnavailable
	}
	defer func() { _, _, _ = procReleaseDC.Call(hwnd, windowDC) }()
	memoryDC, _, callErr := procCreateCompatibleDC.Call(windowDC)
	if memoryDC == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return callErr
		}
		return ErrScreenshotUnavailable
	}
	defer func() { _, _, _ = procDeleteDC.Call(memoryDC) }()

	bmi := bitmapInfo{Header: bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(width),
		Height:      -int32(height),
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}}
	var bits unsafe.Pointer
	bitmap, _, callErr := procCreateDIBSection.Call(windowDC, uintptr(unsafe.Pointer(&bmi)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return callErr
		}
		return ErrScreenshotUnavailable
	}
	defer func() { _, _, _ = procDeleteObject.Call(bitmap) }()
	oldObject, _, _ := procSelectObject.Call(memoryDC, bitmap)
	if oldObject == 0 {
		return ErrScreenshotUnavailable
	}
	defer func() { _, _, _ = procSelectObject.Call(memoryDC, oldObject) }()

	if ok, _, _ := procPrintWindow.Call(hwnd, memoryDC, 0); ok == 0 {
		return ErrScreenshotUnavailable
	}
	raw := unsafe.Slice((*byte)(bits), width*height*4)
	imageData := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			source := (y*width + x) * 4
			target := y*imageData.Stride + x*4
			imageData.Pix[target+0] = raw[source+2]
			imageData.Pix[target+1] = raw[source+1]
			imageData.Pix[target+2] = raw[source+0]
			imageData.Pix[target+3] = 255
		}
	}
	file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	encodeErr := encoder.Encode(file, imageData)
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(outputPath)
		return encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(outputPath)
		return closeErr
	}
	return nil
}
