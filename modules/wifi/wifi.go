package wifi

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wlanapi                         = windows.NewLazySystemDLL("wlanapi.dll")
	procWlanOpenHandle              = wlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle             = wlanapi.NewProc("WlanCloseHandle")
	procWlanEnumInterfaces          = wlanapi.NewProc("WlanEnumInterfaces")
	procWlanScan                    = wlanapi.NewProc("WlanScan")
	procWlanGetAvailableNetworkList = wlanapi.NewProc("WlanGetAvailableNetworkList")
	procWlanFreeMemory              = wlanapi.NewProc("WlanFreeMemory")
	procWlanRegisterNotification    = wlanapi.NewProc("WlanRegisterNotification")
)

const (
	wlanClientVersion = 2

	wlanNotificationSourceACM = 0x00000008
	wlanNotificationACMScanComplete = 7
)

type wlanNotificationData struct {
	NotificationSource uint32
	NotificationCode   uint32
	InterfaceGuid      windows.GUID
	DataSize           uint32
	DataPtr            uintptr
}

type notificationCallback struct {
	ch chan windows.GUID
}

var (
	callbackInstance *notificationCallback
	callbackOnce     sync.Once
)

func notificationProc(data *wlanNotificationData, context uintptr) uintptr {
	if data.NotificationSource == wlanNotificationSourceACM &&
		data.NotificationCode == wlanNotificationACMScanComplete {

		callbackInstance.ch <- data.InterfaceGuid
	}
	return 0
}

func registerNotification(handle windows.Handle, ch chan windows.GUID) error {
	callbackOnce.Do(func() {
		callbackInstance = &notificationCallback{ch: ch}
	})

	cb := windows.NewCallback(notificationProc)

	r1, _, err := procWlanRegisterNotification.Call(
		uintptr(handle),
		uintptr(wlanNotificationSourceACM),
		0,
		cb,
		0,
		0,
		0,
	)

	if r1 != 0 {
		return err
	}
	return nil
}

func openHandle() (windows.Handle, error) {
	var negotiated uint32
	var handle windows.Handle

	r1, _, err := procWlanOpenHandle.Call(
		uintptr(wlanClientVersion),
		0,
		uintptr(unsafe.Pointer(&negotiated)),
		uintptr(unsafe.Pointer(&handle)),
	)

	if r1 != 0 {
		return 0, err
	}
	return handle, nil
}

func closeHandle(handle windows.Handle) {
	procWlanCloseHandle.Call(uintptr(handle), 0)
}

func enumInterfaces(handle windows.Handle) ([]windows.GUID, error) {
	var listPtr uintptr

	r1, _, err := procWlanEnumInterfaces.Call(
		uintptr(handle),
		0,
		uintptr(unsafe.Pointer(&listPtr)),
	)
	if r1 != 0 {
		return nil, err
	}
	defer procWlanFreeMemory.Call(listPtr)

	type header struct {
		NumberOfItems uint32
		Index         uint32
	}

	type iface struct {
		Guid windows.GUID
		Desc [256]uint16
		State uint32
	}

	h := (*header)(unsafe.Pointer(listPtr))
	if h.NumberOfItems == 0 {
		return nil, errors.New("no wifi interfaces")
	}

	result := make([]windows.GUID, 0, h.NumberOfItems)
	base := listPtr + unsafe.Sizeof(*h)
	itemSize := unsafe.Sizeof(iface{})

	for i := uint32(0); i < h.NumberOfItems; i++ {
		item := (*iface)(unsafe.Pointer(base + uintptr(i)*itemSize))
		result = append(result, item.Guid)
	}

	return result, nil
}

func scan(handle windows.Handle, guid windows.GUID) error {
	r1, _, err := procWlanScan.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&guid)),
		0,
		0,
		0,
	)
	if r1 != 0 {
		return err
	}
	return nil
}

func waitForScan(ch chan windows.GUID, expected int) {
	completed := 0
	for completed < expected {
		<-ch
		completed++
	}
}

func ScanAndList() ([]Network, error) {
	handle, err := openHandle()
	if err != nil {
		return nil, err
	}
	defer closeHandle(handle)

	ifaces, err := enumInterfaces(handle)
	if err != nil {
		return nil, err
	}

	ch := make(chan windows.GUID, len(ifaces))

	if err := registerNotification(handle, ch); err != nil {
		return nil, err
	}

	// Запускаем scan на всех интерфейсах
	for _, guid := range ifaces {
		if err := scan(handle, guid); err != nil {
			return nil, err
		}
	}

	// Ждём завершения сканирования
	waitForScan(ch, len(ifaces))

	// Получаем сети
	var allNetworks []Network

	for _, guid := range ifaces {
		nets, err := getNetworks(handle, guid)
		if err != nil {
			return nil, err
		}
		allNetworks = append(allNetworks, nets...)
	}

	return allNetworks, nil
}