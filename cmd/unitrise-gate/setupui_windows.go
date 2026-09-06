//go:build windows

package main

// Native Win32 windows for the built-in installer - an options dialog
// (desktop shortcut? open the guide?) and a staged progress window. Hand-
// rolled syscalls on purpose: no cgo (the ubuntu release runner keeps
// cross-compiling), no webview, no toolkit - a setup surface this small
// doesn't earn a dependency. Themed controls + DPI awareness come from the
// embedded application manifest (build/app.manifest).

import (
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")

	pRegisterClassEx     = user32.NewProc("RegisterClassExW")
	pCreateWindowEx      = user32.NewProc("CreateWindowExW")
	pDefWindowProc       = user32.NewProc("DefWindowProcW")
	pGetMessage          = user32.NewProc("GetMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessage     = user32.NewProc("DispatchMessageW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pPostMessage         = user32.NewProc("PostMessageW")
	pSendMessage         = user32.NewProc("SendMessageW")
	pShowWindow          = user32.NewProc("ShowWindow")
	pUpdateWindow        = user32.NewProc("UpdateWindow")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pSetWindowText       = user32.NewProc("SetWindowTextW")
	pGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	pLoadCursor          = user32.NewProc("LoadCursorW")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pGetDpiForSystem     = user32.NewProc("GetDpiForSystem")
	pCreateFont          = gdi32.NewProc("CreateFontW")
	pInitCommonControls  = comctl32.NewProc("InitCommonControlsEx")
)

const (
	wsOverlapped   = 0x00000000
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsVisible      = 0x10000000
	wsChild        = 0x40000000
	wsTabstop      = 0x00010000
	bsAutoCheckbox = 0x0003
	bsDefPushbtn   = 0x0001
	ssLeft         = 0x0000

	wmDestroy  = 0x0002
	wmClose    = 0x0010
	wmSetFont  = 0x0030
	wmCommand  = 0x0111
	wmApp      = 0x8000
	wmProgress = wmApp + 1 // wParam = percent, lParam = phase-text index
	wmDone     = wmApp + 2 // wParam = 1 success / 0 failure

	bmSetCheck = 0x00F1
	bmGetCheck = 0x00F0
	bstChecked = 1

	pbmSetRange32 = 0x0406
	pbmSetPos     = 0x0402

	idInstall = 101
	idCancel  = 102
	idChkDesk = 103
	idChkDoc  = 104

	smCxScreen = 0
	smCyScreen = 1

	colorBtnface   = 15
	iccProgressCls = 0x00000020
)

func utf16p(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

type dlgCtx struct {
	scale     float64
	hwnd      windows.Handle
	font      windows.Handle
	titleFont windows.Handle
}

func (d *dlgCtx) px(v int) uintptr { return uintptr(int(float64(v) * d.scale)) }

func systemScale() float64 {
	if err := pGetDpiForSystem.Find(); err == nil { // Win10 1607+
		dpi, _, _ := pGetDpiForSystem.Call()
		if dpi >= 96 {
			return float64(dpi) / 96.0
		}
	}
	return 1.0
}

func initUI(d *dlgCtx) {
	var icc struct {
		Size uint32
		ICC  uint32
	}
	icc.Size = uint32(unsafe.Sizeof(icc))
	icc.ICC = iccProgressCls
	pInitCommonControls.Call(uintptr(unsafe.Pointer(&icc))) //nolint:errcheck
	d.scale = systemScale()
	mk := func(pt, weight int) windows.Handle {
		h, _, _ := pCreateFont.Call(
			uintptr(-int(float64(pt)*d.scale*96/72)), 0, 0, 0, uintptr(weight),
			0, 0, 0, 0, 0, 0, 5 /*CLEARTYPE_QUALITY*/, 0,
			uintptr(unsafe.Pointer(utf16p("Segoe UI"))))
		return windows.Handle(h)
	}
	d.font = mk(9, 400)
	d.titleFont = mk(12, 600)
}

func (d *dlgCtx) child(class, text string, style, x, y, w, h, id uintptr, title bool) windows.Handle {
	hw, _, _ := pCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(utf16p(class))),
		uintptr(unsafe.Pointer(utf16p(text))),
		wsChild|wsVisible|style,
		d.px(int(x)), d.px(int(y)), d.px(int(w)), d.px(int(h)),
		uintptr(d.hwnd), id, 0, 0)
	f := d.font
	if title {
		f = d.titleFont
	}
	pSendMessage.Call(hw, wmSetFont, uintptr(f), 1) //nolint:errcheck
	return windows.Handle(hw)
}

func (d *dlgCtx) window(className string, wndProc uintptr, w, h int, title string) windows.Handle {
	cursor, _, _ := pLoadCursor.Call(0, 32512 /*IDC_ARROW*/)
	var wc struct {
		Size, Style                        uint32
		WndProc                            uintptr
		ClsExtra, WndExtra                 int32
		Instance, Icon, Cursor, Background windows.Handle
		MenuName, ClassName                *uint16
		IconSm                             windows.Handle
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	wc.WndProc = wndProc
	wc.Cursor = windows.Handle(cursor)
	wc.Background = windows.Handle(colorBtnface + 1)
	wc.ClassName = utf16p(className)
	pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))) //nolint:errcheck

	sw, _, _ := pGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := pGetSystemMetrics.Call(smCyScreen)
	ww, wh := d.px(w), d.px(h)
	x := (sw - ww) / 2
	y := (sh - wh) / 2
	hw, _, _ := pCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(utf16p(className))),
		uintptr(unsafe.Pointer(utf16p(title))),
		wsOverlapped|wsCaption|wsSysMenu,
		x, y, ww, wh, 0, 0, 0, 0)
	d.hwnd = windows.Handle(hw)
	return d.hwnd
}

func msgLoop() {
	var msg struct {
		Hwnd    windows.Handle
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Pt      struct{ X, Y int32 }
	}
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg))) //nolint:errcheck
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))  //nolint:errcheck
	}
}

type installOpts struct {
	DesktopShortcut bool
	OpenGuide       bool
}

// setupDialog shows the pre-install options window. Returns proceed=false on
// Cancel/close.
func setupDialog() (proceed bool, opts installOpts) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d := &dlgCtx{}
	initUI(d)

	opts = installOpts{DesktopShortcut: true, OpenGuide: true}
	var chkDesk, chkDoc windows.Handle
	done := make(chan struct{})

	checked := func(h windows.Handle) bool {
		r, _, _ := pSendMessage.Call(uintptr(h), bmGetCheck, 0, 0)
		return r == bstChecked
	}
	wndProc := windows.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
		switch msg {
		case wmCommand:
			switch wParam & 0xFFFF {
			case idInstall:
				proceed = true
				opts.DesktopShortcut = checked(chkDesk)
				opts.OpenGuide = checked(chkDoc)
				pDestroyWindow.Call(hwnd) //nolint:errcheck
			case idCancel:
				pDestroyWindow.Call(hwnd) //nolint:errcheck
			}
			return 0
		case wmClose:
			pDestroyWindow.Call(hwnd) //nolint:errcheck
			return 0
		case wmDestroy:
			pPostQuitMessage.Call(0) //nolint:errcheck
			close(done)
			return 0
		}
		r, _, _ := pDefWindowProc.Call(hwnd, msg, wParam, lParam)
		return r
	})

	hwnd := d.window("UnitRiseGateSetup", wndProc, 480, 322, "UnitRise Gate Bridge Setup")
	d.child("STATIC", "UnitRise Gate Bridge", ssLeft, 24, 18, 420, 28, 0, true)
	d.child("STATIC",
		"Keeps your gate system's code list in sync with UnitRise - move-ins, suspensions, and move-outs reach the gate automatically.\n\nSetup installs a background service and a status icon by the clock, then opens the connection page for your credentials.",
		ssLeft, 24, 52, 420, 118, 0, false)
	chkDesk = d.child("BUTTON", "Create a desktop shortcut", bsAutoCheckbox|wsTabstop, 24, 178, 420, 22, idChkDesk, false)
	chkDoc = d.child("BUTTON", "Open the quick-start guide when done", bsAutoCheckbox|wsTabstop, 24, 204, 420, 22, idChkDoc, false)
	pSendMessage.Call(uintptr(chkDesk), bmSetCheck, bstChecked, 0) //nolint:errcheck
	pSendMessage.Call(uintptr(chkDoc), bmSetCheck, bstChecked, 0)  //nolint:errcheck
	d.child("BUTTON", "Install", bsDefPushbtn|wsTabstop, 258, 242, 96, 30, idInstall, false)
	d.child("BUTTON", "Cancel", wsTabstop, 362, 242, 96, 30, idCancel, false)

	pShowWindow.Call(uintptr(hwnd), 1)       //nolint:errcheck
	pUpdateWindow.Call(uintptr(hwnd))        //nolint:errcheck
	pSetForegroundWindow.Call(uintptr(hwnd)) //nolint:errcheck
	msgLoop()
	<-done
	return proceed, opts
}

// progressDialog runs work in the background while a staged progress bar
// marches; the window guarantees a minimum on-screen life of ~2s so a fast
// install doesn't just blink. work reports (percent, phase text) as it goes.
// Returns work's error (already shown to the user as a message box).
func progressDialog(work func(report func(pct int, msg string)) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d := &dlgCtx{}
	initUI(d)

	var (
		bar, label windows.Handle
		phasesMu   sync.Mutex
		phases     []string
		workErr    error
		done       = make(chan struct{})
	)

	wndProc := windows.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
		switch msg {
		case wmProgress:
			pSendMessage.Call(uintptr(bar), pbmSetPos, wParam, 0) //nolint:errcheck
			phasesMu.Lock()
			if int(lParam) < len(phases) {
				pSetWindowText.Call(uintptr(label), uintptr(unsafe.Pointer(utf16p(phases[lParam])))) //nolint:errcheck
			}
			phasesMu.Unlock()
			return 0
		case wmDone:
			pDestroyWindow.Call(hwnd) //nolint:errcheck
			return 0
		case wmClose:
			return 0 // no closing a half-done install from the title bar
		case wmDestroy:
			pPostQuitMessage.Call(0) //nolint:errcheck
			close(done)
			return 0
		}
		r, _, _ := pDefWindowProc.Call(hwnd, msg, wParam, lParam)
		return r
	})

	hwnd := d.window("UnitRiseGateProgress", wndProc, 480, 200, "Installing UnitRise Gate Bridge")
	d.child("STATIC", "UnitRise Gate Bridge", ssLeft, 24, 18, 420, 28, 0, true)
	label = d.child("STATIC", "Preparing…", ssLeft, 24, 56, 420, 22, 0, false)
	bar = d.child("msctls_progress32", "", 0, 24, 86, 420, 18, 0, false)
	pSendMessage.Call(uintptr(bar), pbmSetRange32, 0, 100) //nolint:errcheck

	pShowWindow.Call(uintptr(hwnd), 1)       //nolint:errcheck
	pUpdateWindow.Call(uintptr(hwnd))        //nolint:errcheck
	pSetForegroundWindow.Call(uintptr(hwnd)) //nolint:errcheck

	started := time.Now()
	go func() {
		report := func(pct int, msg string) {
			phasesMu.Lock()
			phases = append(phases, msg)
			idx := len(phases) - 1
			phasesMu.Unlock()
			pPostMessage.Call(uintptr(hwnd), wmProgress, uintptr(pct), uintptr(idx)) //nolint:errcheck
			// Staged pacing: each phase visibly registers even when the work
			// behind it is instant.
			time.Sleep(250 * time.Millisecond)
		}
		workErr = work(report)
		if workErr == nil {
			report(100, "Done.")
		}
		// The 2-second floor: a too-fast install reads as "did anything
		// happen?" - hold the window until it has lived a moment.
		if remain := 2*time.Second - time.Since(started); remain > 0 {
			time.Sleep(remain)
		}
		ok := uintptr(1)
		if workErr != nil {
			ok = 0
		}
		pPostMessage.Call(uintptr(hwnd), wmDone, ok, 0) //nolint:errcheck
	}()

	msgLoop()
	<-done
	if workErr != nil {
		msgBox("Install failed: "+workErr.Error(), mbOK|mbIconError)
	}
	return workErr
}
