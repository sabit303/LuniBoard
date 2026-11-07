package windows

import (
	"fmt"
	"unsafe"

	winapi "golang.org/x/sys/windows"
)

var (
	user32                      = winapi.NewLazySystemDLL("user32.dll")
	kernel32                    = winapi.NewLazySystemDLL("kernel32.dll")
	procCreateWindowEx          = user32.NewProc("CreateWindowExW")
	procDestroyWindow           = user32.NewProc("DestroyWindow")
	procOpenClipboard           = user32.NewProc("OpenClipboard")
	procCloseClipboard          = user32.NewProc("CloseClipboard")
	procIsClipboardFormat       = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardData        = user32.NewProc("GetClipboardData")
	procEmptyClipboard          = user32.NewProc("EmptyClipboard")
	procSetClipboardData        = user32.NewProc("SetClipboardData")
	procRegisterClipboardFormat = user32.NewProc("RegisterClipboardFormatW")
	procGlobalAlloc             = kernel32.NewProc("GlobalAlloc")
	procGlobalFree              = kernel32.NewProc("GlobalFree")
	procGlobalLock              = kernel32.NewProc("GlobalLock")
	procGlobalUnlock            = kernel32.NewProc("GlobalUnlock")
	procGlobalSize              = kernel32.NewProc("GlobalSize")
	procMoveMemory              = kernel32.NewProc("RtlMoveMemory")
)

type win32 struct{}

// New creates a native adapter without opening or changing the clipboard.
func New(maxBytes int64) (*Adapter, error) {
	if maxBytes < 1 || maxBytes > (int64(^uint(0)>>1)-2)/2 {
		return nil, fmt.Errorf("invalid Windows clipboard byte limit: %d", maxBytes)
	}
	for _, proc := range []*winapi.LazyProc{
		procCreateWindowEx, procDestroyWindow, procOpenClipboard, procCloseClipboard,
		procIsClipboardFormat, procGetClipboardData, procEmptyClipboard, procSetClipboardData,
		procRegisterClipboardFormat, procGlobalAlloc, procGlobalFree, procGlobalLock,
		procGlobalUnlock, procGlobalSize, procMoveMemory,
	} {
		if err := proc.Find(); err != nil {
			return nil, unavailable("load Win32 clipboard API", err)
		}
	}
	a := &Adapter{api: win32{}, maxBytes: maxBytes}
	for _, name := range []string{"ExcludeClipboardContentFromMonitorProcessing", "Clipboard Viewer Ignore"} {
		format, err := registerFormat(name)
		if err != nil {
			return nil, unavailable("RegisterClipboardFormatW", err)
		}
		a.sensitiveFormats = append(a.sensitiveFormats, format)
	}
	for _, name := range []string{"CanIncludeInClipboardHistory", "CanUploadToCloudClipboard"} {
		format, err := registerFormat(name)
		if err != nil {
			return nil, unavailable("RegisterClipboardFormatW", err)
		}
		a.privacyFormats = append(a.privacyFormats, format)
	}
	return a, nil
}

func registerFormat(name string) (uint32, error) {
	ptr, err := winapi.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	format, _, callErr := procRegisterClipboardFormat.Call(uintptr(unsafe.Pointer(ptr)))
	if format == 0 {
		return 0, nativeError(callErr)
	}
	return uint32(format), nil
}

func (win32) createWindow() (uintptr, error) {
	class, _ := winapi.UTF16PtrFromString("STATIC")
	name, _ := winapi.UTF16PtrFromString("LuniBoard clipboard")
	// HWND_MESSAGE (-3) creates an invisible, message-only owner window.
	window, _, err := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(class)),
		uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	if window == 0 {
		return 0, nativeError(err)
	}
	return window, nil
}

func (win32) destroyWindow(window uintptr) error {
	result, _, err := procDestroyWindow.Call(window)
	if result == 0 {
		return nativeError(err)
	}
	return nil
}

func (win32) openClipboard(owner uintptr) error {
	result, _, err := procOpenClipboard.Call(owner)
	if result == 0 {
		return nativeError(err)
	}
	return nil
}

func (win32) closeClipboard() error {
	result, _, err := procCloseClipboard.Call()
	if result == 0 {
		return nativeError(err)
	}
	return nil
}

func (win32) formatAvailable(format uint32) bool {
	result, _, _ := procIsClipboardFormat.Call(uintptr(format))
	return result != 0
}

func (win32) readData(format uint32, maxBytes int64) (data []byte, err error) {
	memory, _, callErr := procGetClipboardData.Call(uintptr(format))
	if memory == 0 {
		return nil, nativeError(callErr)
	}
	size, _, callErr := procGlobalSize.Call(memory)
	if size == 0 {
		return nil, nativeError(callErr)
	}
	ptr, _, callErr := procGlobalLock.Call(memory)
	if ptr == 0 {
		return nil, nativeError(callErr)
	}
	defer func() {
		if unlockErr := unlock(memory); unlockErr != nil && err == nil {
			err = unlockErr
		}
	}()
	data = make([]byte, int(min(uint64(size), uint64(maxBytes))))
	// Copy while locked; never retain pointers to Windows-owned memory.
	procMoveMemory.Call(uintptr(unsafe.Pointer(&data[0])), ptr, uintptr(len(data)))
	return data, nil
}

func (w win32) allocData(data []byte) (memory uintptr, err error) {
	const moveable = 0x0002 // GMEM_MOVEABLE is required for clipboard ownership transfer.
	memory, _, err = procGlobalAlloc.Call(moveable, uintptr(len(data)))
	if memory == 0 {
		return 0, nativeError(err)
	}
	ptr, _, callErr := procGlobalLock.Call(memory)
	if ptr == 0 {
		_ = w.freeData(memory)
		return 0, nativeError(callErr)
	}
	procMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	if err := unlock(memory); err != nil {
		_ = w.freeData(memory)
		return 0, err
	}
	return memory, nil
}

func unlock(memory uintptr) error {
	result, _, err := procGlobalUnlock.Call(memory)
	// Zero also means success when the lock count reaches zero.
	if result == 0 && err != winapi.ERROR_SUCCESS {
		return nativeError(err)
	}
	return nil
}

func (win32) freeData(memory uintptr) error {
	result, _, err := procGlobalFree.Call(memory)
	if result != 0 {
		return nativeError(err)
	}
	return nil
}

func (win32) emptyClipboard() error {
	result, _, err := procEmptyClipboard.Call()
	if result == 0 {
		return nativeError(err)
	}
	return nil
}

func (win32) setData(format uint32, memory uintptr) error {
	result, _, err := procSetClipboardData.Call(uintptr(format), memory)
	if result == 0 {
		return nativeError(err)
	}
	return nil
}

func nativeError(err error) error {
	if err == nil || err == winapi.ERROR_SUCCESS {
		return winapi.ERROR_GEN_FAILURE
	}
	return err
}
