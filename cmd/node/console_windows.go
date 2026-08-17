//go:build windows

package main

import "syscall"

const swHide = 0

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	getConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	user32           = syscall.NewLazyDLL("user32.dll")
	showWindow       = user32.NewProc("ShowWindow")
)

// hideConsoleWindow keeps the UI usable when a console-subsystem binary is
// launched by Explorer or by the HKCU Run key. Release builds can still use
// the windowsgui linker subsystem, but hiding here also makes source builds
// and older installations behave the same way.
func hideConsoleWindow() {
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	_, _, _ = showWindow.Call(hwnd, swHide)
}
