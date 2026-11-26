//go:build windows

package main

import (
	"context"
	"runtime"
	"syscall"
	"unsafe"
)

// A message-only window keeps the tray independent of capture and HTTP work.
var user32 = syscall.NewLazyDLL("user32.dll")
var shell32 = syscall.NewLazyDLL("shell32.dll")

type point struct{ X, Y int32 }
type windowClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type notifyIcon struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	Title               [64]uint16
	InfoFlags           uint32
	GUID                [16]byte
	Balloon             uintptr
}
type winMessage struct {
	Window  uintptr
	Message uint32
	W, L    uintptr
	Time    uint32
	Pos     point
	Private uint32
}

func runTray(ctx context.Context, open func(), quit func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	const callback = 0x8001
	className, _ := syscall.UTF16PtrFromString("FrameLink.Tray")
	menu, _, _ := user32.NewProc("CreatePopupMenu").Call()
	if menu == 0 {
		return
	}
	defer user32.NewProc("DestroyMenu").Call(menu)
	for i, title := range []string{"Открыть панель", "Завершить FrameLink"} {
		label, _ := syscall.UTF16PtrFromString(title)
		user32.NewProc("AppendMenuW").Call(menu, 0, uintptr(i+1), uintptr(unsafe.Pointer(label)))
	}
	proc := syscall.NewCallback(func(hwnd uintptr, msg uint32, w, l uintptr) uintptr {
		switch msg {
		case callback:
			if l == 0x0203 {
				open()
			}
			if l == 0x0205 {
				var pt point
				user32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&pt)))
				user32.NewProc("SetForegroundWindow").Call(hwnd)
				cmd, _, _ := user32.NewProc("TrackPopupMenu").Call(menu, 0x102, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
				if cmd == 1 {
					open()
				}
				if cmd == 2 {
					quit()
				}
			}
			return 0
		case 0x0010:
			user32.NewProc("DestroyWindow").Call(hwnd)
			return 0
		case 0x0002:
			user32.NewProc("PostQuitMessage").Call(0)
			return 0
		}
		ret, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), w, l)
		return ret
	})
	instance, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	class := windowClass{Proc: proc, Name: className, Instance: instance}
	class.Size = uint32(unsafe.Sizeof(class))
	atom, _, _ := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return
	}
	defer user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(className)), instance)
	hwnd, _, _ := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, instance, 0)
	if hwnd == 0 {
		return
	}
	icon, _, _ := user32.NewProc("LoadIconW").Call(0, 32512)
	data := notifyIcon{Window: hwnd, ID: 1, Flags: 7, Callback: callback, Icon: icon}
	data.Size = uint32(unsafe.Sizeof(data))
	tip, _ := syscall.UTF16FromString("FrameLink")
	copy(data.Tip[:], tip)
	notify := shell32.NewProc("Shell_NotifyIconW")
	notify.Call(0, uintptr(unsafe.Pointer(&data)))
	defer notify.Call(2, uintptr(unsafe.Pointer(&data)))
	go func() { <-ctx.Done(); user32.NewProc("PostMessageW").Call(hwnd, 0x0010, 0, 0) }()
	var m winMessage
	for {
		v, _, _ := user32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(v) <= 0 {
			return
		}
		user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&m)))
		user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&m)))
	}
}
