# studio-display-ctl

CLI tool to control Apple Studio Display brightness on Windows via HID.

No CGO, no third-party HID libraries — calls Windows `hid.dll` and `setupapi.dll` directly.

## Requirements

- Windows 10 / 11
- Go 1.21+
- Apple Studio Display connected via Thunderbolt / USB-C

## Build

```powershell
go mod tidy
go build -o studio_display.exe .
```

## Usage

```powershell
# Get current brightness (0–100)
.\studio_display.exe get

# Set brightness to 80%
.\studio_display.exe set 80

# Increase brightness by 10 (default step)
.\studio_display.exe up

# Decrease brightness by 5
.\studio_display.exe down --step 5

# Target a specific display by serial number
.\studio_display.exe --serial <SN> set 50
```

## How it works

Windows exposes the Apple Studio Display as a HID device. This tool:

1. Enumerates HID devices via `SetupDiGetClassDevs` / `SetupDiEnumDeviceInterfaces`
2. Filters by Apple's Vendor ID (`0x05AC`) and Studio Display Product ID (`0x1114`), interface `0x07`
3. Reads / writes brightness using `HidD_GetFeature` / `HidD_SetFeature` with a 7-byte report

Brightness values are mapped between the raw hardware range (`400–60000`) and a human-friendly percentage (`0–100%`).