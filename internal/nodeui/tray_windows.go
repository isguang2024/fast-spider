//go:build windows

package nodeui

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

const (
	trayCallbackMessage = win.WM_APP + 37
	trayMenuOpenID      = 1001
	trayMenuExitID      = 1002
	mfString            = 0x00000000
	mfSeparator         = 0x00000800
)

var (
	trayClassOnce   sync.Once
	trayClassName   *uint16
	trayClassErr    error
	trayWindows     sync.Map
	appendMenuWProc = windows.NewLazySystemDLL("user32.dll").NewProc("AppendMenuW")
)

type trayController struct {
	hwnd           win.HWND
	nid            win.NOTIFYICONDATA
	onOpen         func()
	onExit         func()
	logger         *slog.Logger
	taskbarCreated uint32
	exitOnce       sync.Once
}

type trayStartResult struct {
	controller *trayController
	err        error
}

func traySupported() bool { return true }

func startTray(ctx context.Context, onOpen, onExit func(), logger *slog.Logger) (func(), error) {
	if logger == nil {
		logger = slog.Default()
	}
	ready := make(chan trayStartResult, 1)
	go runTrayLoop(ready, onOpen, onExit, logger)

	var result trayStartResult
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result = <-ready:
	}
	if result.err != nil {
		return nil, result.err
	}

	stopOnce := sync.Once{}
	stop := func() {
		stopOnce.Do(func() {
			if result.controller != nil && result.controller.hwnd != 0 {
				win.PostMessage(result.controller.hwnd, win.WM_CLOSE, 0, 0)
			}
		})
	}
	go func() {
		<-ctx.Done()
		stop()
	}()
	return stop, nil
}

func runTrayLoop(ready chan<- trayStartResult, onOpen, onExit func(), logger *slog.Logger) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className, err := ensureTrayWindowClass()
	if err != nil {
		ready <- trayStartResult{err: err}
		return
	}
	windowName, err := windows.UTF16PtrFromString("Fast Spider Node Tray")
	if err != nil {
		ready <- trayStartResult{err: err}
		return
	}
	hwnd := win.CreateWindowEx(0, className, windowName, win.WS_OVERLAPPED, 0, 0, 0, 0, 0, 0, win.GetModuleHandle(nil), nil)
	if hwnd == 0 {
		ready <- trayStartResult{err: errors.New("create Fast Spider tray window failed")}
		return
	}

	controller := &trayController{
		hwnd:   hwnd,
		onOpen: onOpen,
		onExit: onExit,
		logger: logger,
	}
	if taskbarMessage, taskbarErr := windows.UTF16PtrFromString("TaskbarCreated"); taskbarErr == nil {
		controller.taskbarCreated = win.RegisterWindowMessage(taskbarMessage)
	}
	trayWindows.Store(hwnd, controller)
	defer trayWindows.Delete(hwnd)
	defer win.DestroyWindow(hwnd)

	if err := controller.addIcon(); err != nil {
		ready <- trayStartResult{err: err}
		return
	}
	defer win.Shell_NotifyIcon(win.NIM_DELETE, &controller.nid)
	ready <- trayStartResult{controller: controller}

	var msg win.MSG
	for {
		status := win.GetMessage(&msg, 0, 0, 0)
		if status <= 0 {
			return
		}
		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}
}

func ensureTrayWindowClass() (*uint16, error) {
	trayClassOnce.Do(func() {
		var err error
		trayClassName, err = windows.UTF16PtrFromString("FastSpiderNodeTrayWindow")
		if err != nil {
			trayClassErr = err
			return
		}
		hInstance := win.GetModuleHandle(nil)
		windowClass := win.WNDCLASSEX{
			CbSize:        uint32(unsafe.Sizeof(win.WNDCLASSEX{})),
			LpfnWndProc:   syscall.NewCallback(trayWindowProc),
			HInstance:     hInstance,
			HIcon:         win.LoadIcon(0, win.MAKEINTRESOURCE(win.IDI_APPLICATION)),
			HCursor:       win.LoadCursor(0, win.MAKEINTRESOURCE(win.IDC_ARROW)),
			LpszClassName: trayClassName,
		}
		if win.RegisterClassEx(&windowClass) == 0 {
			trayClassErr = errors.New("register Fast Spider tray window class failed")
		}
	})
	return trayClassName, trayClassErr
}

func trayWindowProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	value, ok := trayWindows.Load(hwnd)
	if !ok {
		return win.DefWindowProc(hwnd, msg, wParam, lParam)
	}
	controller := value.(*trayController)
	if controller.taskbarCreated != 0 && msg == controller.taskbarCreated {
		if err := controller.addIcon(); err != nil && controller.logger != nil {
			controller.logger.Warn("restore Fast Spider tray icon failed", "error", err)
		}
		return 0
	}
	switch msg {
	case trayCallbackMessage:
		switch uint32(lParam) {
		case win.WM_LBUTTONDBLCLK:
			controller.openUI()
		case win.WM_RBUTTONUP, win.WM_CONTEXTMENU:
			controller.showMenu()
		}
		return 0
	case win.WM_COMMAND:
		controller.handleCommand(uint32(wParam & 0xffff))
		return 0
	case win.WM_CLOSE:
		win.DestroyWindow(hwnd)
		return 0
	case win.WM_DESTROY:
		win.PostQuitMessage(0)
		return 0
	default:
		return win.DefWindowProc(hwnd, msg, wParam, lParam)
	}
}

func (c *trayController) addIcon() error {
	nid := win.NOTIFYICONDATA{
		CbSize:           uint32(unsafe.Sizeof(win.NOTIFYICONDATA{})),
		HWnd:             c.hwnd,
		UID:              1,
		UFlags:           win.NIF_MESSAGE | win.NIF_ICON | win.NIF_TIP,
		UCallbackMessage: trayCallbackMessage,
		HIcon:            win.LoadIcon(0, win.MAKEINTRESOURCE(win.IDI_APPLICATION)),
	}
	tip, err := windows.UTF16FromString("Fast Spider Node")
	if err != nil {
		return err
	}
	copy(nid.SzTip[:], tip)
	if !win.Shell_NotifyIcon(win.NIM_ADD, &nid) {
		return errors.New("add Fast Spider tray icon failed")
	}
	c.nid = nid
	return nil
}

func (c *trayController) openUI() {
	if c.onOpen == nil {
		return
	}
	go c.onOpen()
}

func (c *trayController) showMenu() {
	menu := win.CreatePopupMenu()
	if menu == 0 {
		return
	}
	defer win.DestroyMenu(menu)
	if !appendTrayMenu(menu, mfString, trayMenuOpenID, "打开 Fast Spider") {
		return
	}
	_ = appendTrayMenu(menu, mfSeparator, 0, "")
	if !appendTrayMenu(menu, mfString, trayMenuExitID, "退出 Fast Spider") {
		return
	}

	var point win.POINT
	if !win.GetCursorPos(&point) {
		return
	}
	win.SetForegroundWindow(c.hwnd)
	command := win.TrackPopupMenu(menu, win.TPM_RETURNCMD|win.TPM_RIGHTBUTTON, point.X, point.Y, 0, c.hwnd, nil)
	win.PostMessage(c.hwnd, win.WM_NULL, 0, 0)
	c.handleCommand(command)
}

func (c *trayController) handleCommand(command uint32) {
	switch command {
	case trayMenuOpenID:
		c.openUI()
	case trayMenuExitID:
		c.exitOnce.Do(func() {
			if c.onExit != nil {
				go c.onExit()
			}
		})
	}
}

func appendTrayMenu(menu win.HMENU, flags uint32, id uintptr, label string) bool {
	var labelPtr *uint16
	if flags&mfSeparator == 0 {
		value, err := windows.UTF16PtrFromString(label)
		if err != nil {
			return false
		}
		labelPtr = value
	}
	ret, _, _ := appendMenuWProc.Call(uintptr(menu), uintptr(flags), id, uintptr(unsafe.Pointer(labelPtr)))
	return ret != 0
}
