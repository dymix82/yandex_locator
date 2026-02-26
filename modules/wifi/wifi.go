//go:build windows

package wifi

import (
	"errors"

	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wlanapi                          = windows.NewLazySystemDLL("wlanapi.dll")
	procWlanOpenHandle               = wlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle              = wlanapi.NewProc("WlanCloseHandle")
	procWlanEnumInterfaces           = wlanapi.NewProc("WlanEnumInterfaces")
	procWlanScan                     = wlanapi.NewProc("WlanScan")
	procWlanGetAvailableNetworkList  = wlanapi.NewProc("WlanGetAvailableNetworkList")
	procWlanFreeMemory               = wlanapi.NewProc("WlanFreeMemory")
)

const (
	wlanClientVersionXP = 1
	wlanClientVersion   = 2
)

type wlanInterfaceInfo struct {
	InterfaceGuid windows.GUID
	Description   [256]uint16
	State         uint32
}

type wlanInterfaceInfoList struct {
	NumberOfItems uint32
	Index         uint32
	// Далее идёт массив переменной длины
}

type wlanAvailableNetwork struct {
	ProfileName           [256]uint16
	Ssid                  dot11SSID
	BssType               uint32
	NumberOfBssids        uint32
	Connectable           int32
	WlanNotConnectableReason uint32
	NumberOfPhyTypes      uint32
	PhyTypes              [8]uint32
	MorePhyTypes          uint32
	SignalQuality         uint32
	SecurityEnabled       int32
	DefaultAuthAlgorithm  uint32
	DefaultCipherAlgorithm uint32
	Flags                 uint32
	Reserved              uint32
}

type wlanAvailableNetworkList struct {
	NumberOfItems uint32
	Index         uint32
}

type dot11SSID struct {
	SSIDLength uint32
	SSID       [32]byte
}

type Network struct {
	SSID          string
	SignalQuality uint32
	Security      bool
}

func utf16ToString(s []uint16) string {
	return windows.UTF16ToString(s)
}

func ssidToString(ssid dot11SSID) string {
	return string(ssid.SSID[:ssid.SSIDLength])
}

func OpenHandle() (windows.Handle, error) {
	var negotiatedVersion uint32
	var handle windows.Handle

	r1, _, err := procWlanOpenHandle.Call(
		uintptr(wlanClientVersion),
		0,
		uintptr(unsafe.Pointer(&negotiatedVersion)),
		uintptr(unsafe.Pointer(&handle)),
	)

	if r1 != 0 {
		return 0, err
	}

	return handle, nil
}

func CloseHandle(handle windows.Handle) {
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

	header := (*wlanInterfaceInfoList)(unsafe.Pointer(listPtr))
	count := header.NumberOfItems

	if count == 0 {
		return nil, errors.New("no WiFi interfaces found")
	}

	result := make([]windows.GUID, 0, count)

	base := listPtr + unsafe.Sizeof(*header)
	itemSize := unsafe.Sizeof(wlanInterfaceInfo{})

	for i := uint32(0); i < count; i++ {
		item := (*wlanInterfaceInfo)(unsafe.Pointer(base + uintptr(i)*itemSize))
		result = append(result, item.InterfaceGuid)
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

func getNetworks(handle windows.Handle, guid windows.GUID) ([]Network, error) {
	var listPtr uintptr

	r1, _, err := procWlanGetAvailableNetworkList.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&guid)),
		0,
		0,
		uintptr(unsafe.Pointer(&listPtr)),
	)

	if r1 != 0 {
		return nil, err
	}
	defer procWlanFreeMemory.Call(listPtr)

	header := (*wlanAvailableNetworkList)(unsafe.Pointer(listPtr))
	count := header.NumberOfItems

	networks := make([]Network, 0, count)

	base := listPtr + unsafe.Sizeof(*header)
	itemSize := unsafe.Sizeof(wlanAvailableNetwork{})

	for i := uint32(0); i < count; i++ {
		item := (*wlanAvailableNetwork)(unsafe.Pointer(base + uintptr(i)*itemSize))

		networks = append(networks, Network{
			SSID:          ssidToString(item.Ssid),
			SignalQuality: item.SignalQuality,
			Security:      item.SecurityEnabled != 0,
		})
	}

	return networks, nil
}

func ScanAndList() ([]Network, error) {
	handle, err := OpenHandle()
	if err != nil {
		return nil, err
	}
	defer CloseHandle(handle)

	ifaces, err := enumInterfaces(handle)
	if err != nil {
		return nil, err
	}

	allNetworks := []Network{}

	for _, guid := range ifaces {
		if err := scan(handle, guid); err != nil {
			return nil, err
		}
	}

	// Асинхронный scan → ждём
	time.Sleep(3 * time.Second)

	for _, guid := range ifaces {
		nets, err := getNetworks(handle, guid)
		if err != nil {
			return nil, err
		}
		allNetworks = append(allNetworks, nets...)
	}

	return allNetworks, nil
}