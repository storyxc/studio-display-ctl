//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ------------------------
// 常量
// ------------------------
const (
	ReportID        = 1
	MinBrightness   = 400
	MaxBrightness   = 60000
	BrightnessRange = MaxBrightness - MinBrightness

	SDProductID   = 0x1114
	SDVendorID    = 0x05AC
	SDInterfaceNr = 0x7
)

// Windows HID GUID
var hidGUID = windows.GUID{
	Data1: 0x4d1e55b2,
	Data2: 0xf16f,
	Data3: 0x11cf,
	Data4: [8]byte{0x88, 0xcb, 0x00, 0x11, 0x11, 0x00, 0x00, 0x30},
}

var (
	modHid                               = windows.NewLazyDLL("hid.dll")
	modSetupapi                          = windows.NewLazyDLL("setupapi.dll")
	procHidD_GetHidGuid                  = modHid.NewProc("HidD_GetHidGuid")
	procHidD_GetAttributes               = modHid.NewProc("HidD_GetAttributes")
	procHidD_GetSerialNumberString       = modHid.NewProc("HidD_GetSerialNumberString")
	procHidD_GetFeature                  = modHid.NewProc("HidD_GetFeature")
	procHidD_SetFeature                  = modHid.NewProc("HidD_SetFeature")
	procSetupDiGetClassDevsW             = modSetupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces      = modSetupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW = modSetupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList     = modSetupapi.NewProc("SetupDiDestroyDeviceInfoList")
)

const (
	DIGCF_PRESENT         = 0x00000002
	DIGCF_DEVICEINTERFACE = 0x00000010
	INVALID_HANDLE_VALUE  = ^uintptr(0)
)

type SP_DEVICE_INTERFACE_DATA struct {
	CbSize             uint32
	InterfaceClassGuid windows.GUID
	Flags              uint32
	Reserved           uintptr
}

type SP_DEVICE_INTERFACE_DETAIL_DATA struct {
	CbSize     uint32
	DevicePath [256 * 2]byte // UTF-16
}

type HIDD_ATTRIBUTES struct {
	Size          uint32
	VendorID      uint16
	ProductID     uint16
	VersionNumber uint16
}

// ------------------------
// 设备封装
// ------------------------
type StudioDisplay struct {
	Path   string
	Serial string
}

type Device struct {
	handle windows.Handle
}

func (d *Device) Close() {
	windows.CloseHandle(d.handle)
}

func getHidGuid() windows.GUID {
	var guid windows.GUID
	procHidD_GetHidGuid.Call(uintptr(unsafe.Pointer(&guid)))
	return guid
}

func listDisplays() ([]StudioDisplay, error) {
	guid := getHidGuid()

	hDevInfo, _, _ := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(&guid)),
		0,
		0,
		DIGCF_PRESENT|DIGCF_DEVICEINTERFACE,
	)
	if hDevInfo == INVALID_HANDLE_VALUE {
		return nil, fmt.Errorf("SetupDiGetClassDevs failed")
	}
	defer procSetupDiDestroyDeviceInfoList.Call(hDevInfo)

	var result []StudioDisplay

	for i := uint32(0); ; i++ {
		var ifData SP_DEVICE_INTERFACE_DATA
		ifData.CbSize = uint32(unsafe.Sizeof(ifData))

		ret, _, _ := procSetupDiEnumDeviceInterfaces.Call(
			hDevInfo,
			0,
			uintptr(unsafe.Pointer(&guid)),
			uintptr(i),
			uintptr(unsafe.Pointer(&ifData)),
		)
		if ret == 0 {
			break
		}

		var detail SP_DEVICE_INTERFACE_DETAIL_DATA
		// On 64-bit: cbSize = 8; on 32-bit: cbSize = 6
		detail.CbSize = 8
		procSetupDiGetDeviceInterfaceDetailW.Call(
			hDevInfo,
			uintptr(unsafe.Pointer(&ifData)),
			uintptr(unsafe.Pointer(&detail)),
			uintptr(unsafe.Sizeof(detail)),
			0,
			0,
		)

		path := windows.UTF16ToString((*[256]uint16)(unsafe.Pointer(&detail.DevicePath[0]))[:])

		h, err := windows.CreateFile(
			windows.StringToUTF16Ptr(path),
			0, // no read/write needed for attributes query
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil,
			windows.OPEN_EXISTING,
			0,
			0,
		)
		if err != nil {
			continue
		}

		var attrs HIDD_ATTRIBUTES
		attrs.Size = uint32(unsafe.Sizeof(attrs))
		procHidD_GetAttributes.Call(uintptr(h), uintptr(unsafe.Pointer(&attrs)))

		if attrs.VendorID != SDVendorID || attrs.ProductID != SDProductID {
			windows.CloseHandle(h)
			continue
		}

		// Read serial
		serialBuf := make([]uint16, 128)
		procHidD_GetSerialNumberString.Call(
			uintptr(h),
			uintptr(unsafe.Pointer(&serialBuf[0])),
			uintptr(len(serialBuf)*2),
		)
		serial := windows.UTF16ToString(serialBuf)

		windows.CloseHandle(h)

		// Filter by interface number via path substring (mi_07 = interface 7)
		if !containsInterface7(path) {
			continue
		}

		result = append(result, StudioDisplay{Path: path, Serial: serial})
	}

	return result, nil
}

// Apple Studio Display interface 7 shows up as "mi_07" in the device path
func containsInterface7(path string) bool {
	for i := 0; i+5 < len(path); i++ {
		if path[i] == 'm' && path[i+1] == 'i' && path[i+2] == '_' &&
			path[i+3] == '0' && path[i+4] == '7' {
			return true
		}
	}
	return false
}

func openDisplay(d StudioDisplay) (*Device, error) {
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(d.Path),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open display: %w", err)
	}
	return &Device{handle: h}, nil
}

func getTargetDisplay(serial string) (StudioDisplay, error) {
	displays, err := listDisplays()
	if err != nil {
		return StudioDisplay{}, err
	}
	if len(displays) == 0 {
		return StudioDisplay{}, fmt.Errorf("no Apple Studio Display found")
	}
	if serial != "" {
		for _, d := range displays {
			if d.Serial == serial {
				return d, nil
			}
		}
		return StudioDisplay{}, fmt.Errorf("no display with serial: %s", serial)
	}
	return displays[0], nil
}

// ------------------------
// 核心逻辑
// ------------------------
func getBrightness(dev *Device) (int, error) {
	buf := make([]byte, 7)
	buf[0] = ReportID

	ret, _, err := procHidD_GetFeature.Call(
		uintptr(dev.handle),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if ret == 0 {
		return 0, fmt.Errorf("HidD_GetFeature failed: %w", err)
	}

	brightness := int(binary.LittleEndian.Uint32(buf[1:5]))
	return brightness, nil
}

func getBrightnessPercent(dev *Device) (int, error) {
	raw, err := getBrightness(dev)
	if err != nil {
		return 0, err
	}
	if raw < MinBrightness {
		raw = MinBrightness
	}
	if raw > MaxBrightness {
		raw = MaxBrightness
	}
	percent := (raw - MinBrightness) * 100 / BrightnessRange
	return percent, nil
}

func setBrightness(dev *Device, brightness int) error {
	if brightness < MinBrightness {
		brightness = MinBrightness
	}
	if brightness > MaxBrightness {
		brightness = MaxBrightness
	}

	buf := make([]byte, 7)
	buf[0] = ReportID
	binary.LittleEndian.PutUint32(buf[1:5], uint32(brightness))

	ret, _, err := procHidD_SetFeature.Call(
		uintptr(dev.handle),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if ret == 0 {
		return fmt.Errorf("HidD_SetFeature failed: %w", err)
	}
	return nil
}

func setBrightnessPercent(dev *Device, percent int) error {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	nits := percent*BrightnessRange/100 + MinBrightness
	return setBrightness(dev, nits)
}

// ------------------------
// CLI
// ------------------------
func usage() {
	fmt.Println(`Usage:
  studio_display [--serial <SN>] get
  studio_display [--serial <SN>] set <0-100>
  studio_display [--serial <SN>] up [--step <n>]
  studio_display [--serial <SN>] down [--step <n>]`)
}

func main() {
	args := os.Args[1:]

	serial := ""
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--serial" {
			serial = args[i+1]
			args = append(args[:i], args[i+2:]...)
			break
		}
	}

	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	cmd := args[0]
	cmdArgs := args[1:]

	d, err := getTargetDisplay(serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	dev, err := openDisplay(d)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	defer dev.Close()

	switch cmd {
	case "get":
		pct, err := getBrightnessPercent(dev)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		fmt.Println("brightness:", pct)

	case "set":
		if len(cmdArgs) < 1 {
			fmt.Fprintln(os.Stderr, "Usage: set <0-100>")
			os.Exit(1)
		}
		val, err := strconv.Atoi(cmdArgs[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, "Invalid value:", cmdArgs[0])
			os.Exit(1)
		}
		if err := setBrightnessPercent(dev, val); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}

	case "up", "down":
		step := 10
		for i := 0; i < len(cmdArgs)-1; i++ {
			if cmdArgs[i] == "--step" {
				step, err = strconv.Atoi(cmdArgs[i+1])
				if err != nil {
					fmt.Fprintln(os.Stderr, "Invalid step:", cmdArgs[i+1])
					os.Exit(1)
				}
			}
		}
		cur, err := getBrightnessPercent(dev)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		var next int
		if cmd == "up" {
			next = cur + step
			if next > 100 {
				next = 100
			}
		} else {
			next = cur - step
			if next < 0 {
				next = 0
			}
		}
		if err := setBrightnessPercent(dev, next); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintln(os.Stderr, "Unknown command:", cmd)
		usage()
		os.Exit(1)
	}
}
