package m55rgbpro

// Package: CORSAIR M55 RGB PRO Gaming Mouse.
// This is the primary package for CORSAIR M55 RGB PRO Gaming Mouse.
// All device actions are controlled from this package.
// Author: Nikola Jurkovic
// License: GPL-3.0 or later

import (
	"OpenLinkHub/src/common"
	"OpenLinkHub/src/config"
	"OpenLinkHub/src/logger"
	"OpenLinkHub/src/rgb"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/sstallion/go-hid"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type ZoneColors struct {
	Color      *rgb.Color
	ColorIndex []int
	Name       string
}

// DeviceProfile struct contains all device profile
type DeviceProfile struct {
	Active           bool
	Path             string
	Product          string
	Serial           string
	Brightness       uint8
	RGBProfile       string
	BrightnessSlider *uint8
	Label            string
	Profile          int
	ZoneColors       map[int]ZoneColors
	Profiles         map[int]DPIProfile
	SleepMode        int
}

type DPIProfile struct {
	Name        string `json:"name"`
	Value       uint16
	PackerIndex int
	ColorIndex  map[int][]int
	Color       *rgb.Color
}

type Device struct {
	Debug                 bool
	dev                   *hid.Device
	listener              *hid.Device
	Manufacturer          string `json:"manufacturer"`
	Product               string `json:"product"`
	Serial                string `json:"serial"`
	Firmware              string `json:"firmware"`
	activeRgb             *rgb.ActiveRGB
	UserProfiles          map[string]*DeviceProfile `json:"userProfiles"`
	Devices               map[int]string            `json:"devices"`
	DeviceProfile         *DeviceProfile
	OriginalProfile       *DeviceProfile
	Template              string
	VendorId              uint16
	ProductId             uint16
	Brightness            map[int]string
	LEDChannels           int
	ChangeableLedChannels int
	CpuTemp               float32
	GpuTemp               float32
	Layouts               []string
	Rgb                   *rgb.RGB
	SleepModes            map[int]string
}

var (
	pwd             = ""
	cmdSoftwareMode = []byte{0x01, 0x03, 0x00, 0x02}
	cmdHardwareMode = []byte{0x01, 0x03, 0x00, 0x01}
	cmdGetFirmware  = []byte{0x02, 0x13}
	cmdWriteColor   = []byte{0x06, 0x00}
	cmdOpenEndpoint = []byte{0x0d, 0x00, 0x01}
	cmdSetDpi       = map[int][]byte{0: {0x01, 0x20, 0x00}}
	mutex           sync.Mutex
	timerKeepAlive  = &time.Ticker{}
	keepAliveChan   = make(chan bool)
	bufferSize      = 128
	bufferSizeWrite = bufferSize + 1
	headerSize      = 2
	headerWriteSize = 4
	minDpiValue     = 200
	maxDpiValue     = 12400
	deviceKeepAlive = 20000
)

func Init(vendorId, productId uint16, key string) *Device {
	// Set global working directory
	pwd = config.GetConfig().ConfigPath

	dev, err := hid.OpenPath(key)
	if err != nil {
		logger.Log(logger.Fields{"error": err, "vendorId": vendorId, "productId": productId}).Error("Unable to open HID device")
		return nil
	}

	// Init new struct with HID device
	d := &Device{
		dev:       dev,
		Template:  "m55rgbpro.html",
		VendorId:  vendorId,
		ProductId: productId,
		Firmware:  "n/a",
		Brightness: map[int]string{
			0: "RGB Profile",
			1: "33 %",
			2: "66 %",
			3: "100 %",
		},
		Product: "M55 RGB PRO",
		SleepModes: map[int]string{
			1:  "1 minute",
			5:  "5 minutes",
			10: "10 minutes",
			15: "15 minutes",
			30: "30 minutes",
			60: "1 hour",
		},
		LEDChannels:           2,
		ChangeableLedChannels: 1,
	}

	d.getDebugMode()       // Debug mode
	d.getManufacturer()    // Manufacturer
	d.getSerial()          // Serial
	d.loadRgb()            // Load RGB
	d.loadDeviceProfiles() // Load all device profiles
	d.saveDeviceProfile()  // Save profile
	d.getDeviceFirmware()  // Firmware
	d.setSoftwareMode()    // Activate software mode
	d.initLeds()           // Init LED ports
	d.setDeviceColor()     // Device color
	d.toggleDPI()          // DPI
	d.controlListener()    // Control listener
	d.setKeepAlive()       // Keepalive
	return d
}

// Stop will stop all device operations and switch a device back to hardware mode
func (d *Device) Stop() {
	logger.Log(logger.Fields{"serial": d.Serial}).Info("Stopping device...")
	if d.activeRgb != nil {
		d.activeRgb.Stop()
	}

	timerKeepAlive.Stop()
	keepAliveChan <- true

	d.setHardwareMode()
	if d.dev != nil {
		err := d.dev.Close()
		if err != nil {
			return
		}
	}

	if d.listener != nil {
		err := d.listener.Close()
		if err != nil {
			return
		}
	}
}

// getManufacturer will return device manufacturer
func (d *Device) getManufacturer() {
	manufacturer, err := d.dev.GetMfrStr()
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Fatal("Unable to get manufacturer")
	}
	d.Manufacturer = manufacturer
}

// getSerial will return device serial number
func (d *Device) getSerial() {
	serial, err := d.dev.GetSerialNbr()
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Fatal("Unable to get device serial number")
	}
	d.Serial = serial
}

// loadRgb will load RGB file if found, or create the default.
func (d *Device) loadRgb() {
	rgbDirectory := pwd + "/database/rgb/"
	rgbFilename := rgbDirectory + d.Serial + ".json"

	// Check if filename has .json extension
	if !common.IsValidExtension(rgbFilename, ".json") {
		return
	}

	if !common.FileExists(rgbFilename) {
		profile := rgb.GetRGB()
		profile.Device = d.Product

		// Convert to JSON
		buffer, err := json.MarshalIndent(profile, "", "    ")
		if err != nil {
			logger.Log(logger.Fields{"error": err, "serial": d.Serial, "location": rgbFilename}).Warn("Unable to encode RGB json")
			return
		}

		// Create profile filename
		file, err := os.Create(rgbFilename)
		if err != nil {
			logger.Log(logger.Fields{"error": err, "serial": d.Serial, "location": rgbFilename}).Warn("Unable to create RGB json file")
			return
		}

		// Write JSON buffer to file
		_, err = file.Write(buffer)
		if err != nil {
			logger.Log(logger.Fields{"error": err, "serial": d.Serial, "location": rgbFilename}).Warn("Unable to write to RGB json file")
			return
		}

		// Close file
		err = file.Close()
		if err != nil {
			logger.Log(logger.Fields{"error": err, "serial": d.Serial, "location": rgbFilename}).Warn("Unable to close RGB json file")
			return
		}
	}

	file, err := os.Open(rgbFilename)
	if err != nil {
		logger.Log(logger.Fields{"error": err, "serial": d.Serial, "location": rgbFilename}).Warn("Unable to load RGB")
		return
	}
	if err = json.NewDecoder(file).Decode(&d.Rgb); err != nil {
		logger.Log(logger.Fields{"error": err, "serial": d.Serial, "location": rgbFilename}).Warn("Unable to decode profile")
		return
	}
	err = file.Close()
	if err != nil {
		logger.Log(logger.Fields{"location": rgbFilename, "serial": d.Serial}).Warn("Failed to close file handle")
	}
}

// GetRgbProfile will return rgb.Profile struct
func (d *Device) GetRgbProfile(profile string) *rgb.Profile {
	if d.Rgb == nil {
		return nil
	}

	if val, ok := d.Rgb.Profiles[profile]; ok {
		return &val
	}
	return nil
}

// GetDeviceTemplate will return device template name
func (d *Device) GetDeviceTemplate() string {
	return d.Template
}

// ChangeDeviceProfile will change device profile
func (d *Device) ChangeDeviceProfile(profileName string) uint8 {
	if profile, ok := d.UserProfiles[profileName]; ok {
		currentProfile := d.DeviceProfile
		currentProfile.Active = false
		d.DeviceProfile = currentProfile
		d.saveDeviceProfile()

		// RGB reset
		if d.activeRgb != nil {
			d.activeRgb.Exit <- true // Exit current RGB mode
			d.activeRgb = nil
		}

		newProfile := profile
		newProfile.Active = true
		d.DeviceProfile = newProfile
		d.saveDeviceProfile()
		d.setDeviceColor()
		return 1
	}
	return 0
}

// UpdateRgbProfile will update device RGB profile
func (d *Device) UpdateRgbProfile(_ int, profile string) uint8 {
	if d.GetRgbProfile(profile) == nil {
		logger.Log(logger.Fields{"serial": d.Serial, "profile": profile}).Warn("Non-existing RGB profile")
		return 0
	}
	d.DeviceProfile.RGBProfile = profile // Set profile
	d.saveDeviceProfile()                // Save profile
	if d.activeRgb != nil {
		d.activeRgb.Exit <- true // Exit current RGB mode
		d.activeRgb = nil
	}
	d.setDeviceColor() // Restart RGB
	return 1
}

// ChangeDeviceBrightness will change device brightness
func (d *Device) ChangeDeviceBrightness(mode uint8) uint8 {
	d.DeviceProfile.Brightness = mode
	d.saveDeviceProfile()
	if d.activeRgb != nil {
		d.activeRgb.Exit <- true // Exit current RGB mode
		d.activeRgb = nil
	}
	d.setDeviceColor() // Restart RGB
	return 1
}

// ChangeDeviceBrightnessValue will change device brightness via slider
func (d *Device) ChangeDeviceBrightnessValue(value uint8) uint8 {
	if value < 0 || value > 100 {
		return 0
	}

	d.DeviceProfile.BrightnessSlider = &value
	d.saveDeviceProfile()

	if d.DeviceProfile.RGBProfile == "static" || d.DeviceProfile.RGBProfile == "mouse" {
		if d.activeRgb != nil {
			d.activeRgb.Exit <- true // Exit current RGB mode
			d.activeRgb = nil
		}
		d.setDeviceColor() // Restart RGB
	}
	return 1
}

// SaveUserProfile will generate a new user profile configuration and save it to a file
func (d *Device) SaveUserProfile(profileName string) uint8 {
	if d.DeviceProfile != nil {
		profilePath := pwd + "/database/profiles/" + d.Serial + "-" + profileName + ".json"

		newProfile := d.DeviceProfile
		newProfile.Path = profilePath
		newProfile.Active = false

		buffer, err := json.Marshal(newProfile)
		if err != nil {
			logger.Log(logger.Fields{"error": err}).Error("Unable to convert to json format")
			return 0
		}

		// Create profile filename
		file, err := os.Create(profilePath)
		if err != nil {
			logger.Log(logger.Fields{"error": err, "location": newProfile.Path}).Error("Unable to create new device profile")
			return 0
		}

		_, err = file.Write(buffer)
		if err != nil {
			logger.Log(logger.Fields{"error": err, "location": newProfile.Path}).Error("Unable to write data")
			return 0
		}

		err = file.Close()
		if err != nil {
			logger.Log(logger.Fields{"error": err, "location": newProfile.Path}).Error("Unable to close file handle")
			return 0
		}
		d.loadDeviceProfiles()
		return 1
	}
	return 0
}

// SaveMouseDPI will save mouse DPI
func (d *Device) SaveMouseDPI(stages map[int]uint16) uint8 {
	i := 0
	if d.DeviceProfile == nil {
		return 0
	}

	if len(stages) == 0 {
		return 0
	}

	for key, stage := range stages {
		if _, ok := d.DeviceProfile.Profiles[key]; ok {
			profile := d.DeviceProfile.Profiles[key]
			if stage > uint16(maxDpiValue) {
				continue
			}
			if stage < uint16(minDpiValue) {
				continue
			}
			profile.Value = stage
			d.DeviceProfile.Profiles[key] = profile
			i++
		}
	}

	if i > 0 {
		d.saveDeviceProfile()
		d.toggleDPI()
		return 1
	}
	return 0
}

// SaveMouseZoneColors will save mouse zone colors
func (d *Device) SaveMouseZoneColors(dpi rgb.Color, zoneColors map[int]rgb.Color) uint8 {
	i := 0
	if d.DeviceProfile == nil {
		return 0
	}
	if dpi.Red > 255 ||
		dpi.Green > 255 ||
		dpi.Blue > 255 ||
		dpi.Red < 0 ||
		dpi.Green < 0 ||
		dpi.Blue < 0 {
		return 0
	}

	// Zone Colors
	for key, zone := range zoneColors {
		if zone.Red > 255 ||
			zone.Green > 255 ||
			zone.Blue > 255 ||
			zone.Red < 0 ||
			zone.Green < 0 ||
			zone.Blue < 0 {
			continue
		}
		if zoneColor, ok := d.DeviceProfile.ZoneColors[key]; ok {
			zoneColor.Color.Red = zone.Red
			zoneColor.Color.Green = zone.Green
			zoneColor.Color.Blue = zone.Blue
			zoneColor.Color.Hex = fmt.Sprintf("#%02x%02x%02x", int(zone.Red), int(zone.Green), int(zone.Blue))
		}
		i++
	}

	if i > 0 {
		d.saveDeviceProfile()
		if d.activeRgb != nil {
			d.activeRgb.Exit <- true // Exit current RGB mode
			d.activeRgb = nil
		}
		d.setDeviceColor() // Restart RGB
		return 1
	}
	return 0
}

// SaveMouseDpiColors will save mouse dpi colors
func (d *Device) SaveMouseDpiColors(dpi rgb.Color, dpiColors map[int]rgb.Color) uint8 {
	i := 0
	if d.DeviceProfile == nil {
		return 0
	}
	if dpi.Red > 255 ||
		dpi.Green > 255 ||
		dpi.Blue > 255 ||
		dpi.Red < 0 ||
		dpi.Green < 0 ||
		dpi.Blue < 0 {
		return 0
	}

	// Zone Colors
	for key, zone := range dpiColors {
		if zone.Red > 255 ||
			zone.Green > 255 ||
			zone.Blue > 255 ||
			zone.Red < 0 ||
			zone.Green < 0 ||
			zone.Blue < 0 {
			continue
		}
		if profileColor, ok := d.DeviceProfile.Profiles[key]; ok {
			profileColor.Color.Red = zone.Red
			profileColor.Color.Green = zone.Green
			profileColor.Color.Blue = zone.Blue
			profileColor.Color.Hex = fmt.Sprintf("#%02x%02x%02x", int(zone.Red), int(zone.Green), int(zone.Blue))
		}
		i++
	}

	if i > 0 {
		d.saveDeviceProfile()
		if d.activeRgb != nil {
			d.activeRgb.Exit <- true // Exit current RGB mode
			d.activeRgb = nil
		}
		d.setDeviceColor() // Restart RGB
		return 1
	}
	return 0
}

// getManufacturer will return device manufacturer
func (d *Device) getDebugMode() {
	d.Debug = config.GetConfig().Debug
}

// setHardwareMode will switch a device to hardware mode
func (d *Device) setHardwareMode() {
	_, err := d.transfer(cmdHardwareMode, nil)
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to change device mode")
	}
}

// setSoftwareMode will switch a device to software mode
func (d *Device) setSoftwareMode() {
	_, err := d.transfer(cmdSoftwareMode, nil)
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to change device mode")
	}
}

// GetSleepMode will return current sleep mode
func (d *Device) GetSleepMode() int {
	if d.DeviceProfile != nil {
		return d.DeviceProfile.SleepMode
	}
	return 0
}

// getDeviceFirmware will return a device firmware version out as string
func (d *Device) getDeviceFirmware() {
	fw, err := d.transfer(
		cmdGetFirmware,
		nil,
	)
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to write to a device")
	}

	v1, v2, v3 := int(fw[3]), int(fw[4]), int(binary.LittleEndian.Uint16(fw[5:7]))
	d.Firmware = fmt.Sprintf("%d.%d.%d", v1, v2, v3)
}

// saveDeviceProfile will save device profile for persistent configuration
func (d *Device) saveDeviceProfile() {
	var defaultBrightness = uint8(100)
	profilePath := pwd + "/database/profiles/" + d.Serial + ".json"

	deviceProfile := &DeviceProfile{
		Product:          d.Product,
		Serial:           d.Serial,
		Path:             profilePath,
		BrightnessSlider: &defaultBrightness,
	}

	// First save, assign saved profile to a device
	if d.DeviceProfile == nil {
		// RGB, Label
		deviceProfile.RGBProfile = "mouse"
		deviceProfile.Label = "Mouse"
		deviceProfile.Active = true
		deviceProfile.ZoneColors = map[int]ZoneColors{
			0: { // Logo
				ColorIndex: []int{1, 3, 5},
				Color: &rgb.Color{
					Red:        255,
					Green:      255,
					Blue:       0,
					Brightness: 1,
					Hex:        fmt.Sprintf("#%02x%02x%02x", 255, 255, 0),
				},
				Name: "Logo",
			},
		}
		deviceProfile.Profiles = map[int]DPIProfile{
			0: {
				Name:        "Stage 1",
				Value:       200,
				PackerIndex: 1,
				ColorIndex: map[int][]int{
					0: {0, 2, 4},
				},
				Color: &rgb.Color{
					Red:        255,
					Green:      0,
					Blue:       0,
					Brightness: 1,
					Hex:        fmt.Sprintf("#%02x%02x%02x", 255, 0, 0),
				},
			},
			1: {
				Name:        "Stage 2",
				Value:       1500,
				PackerIndex: 2,
				ColorIndex: map[int][]int{
					0: {0, 2, 4},
				},
				Color: &rgb.Color{
					Red:        255,
					Green:      255,
					Blue:       255,
					Brightness: 1,
					Hex:        fmt.Sprintf("#%02x%02x%02x", 255, 255, 255),
				},
			},
			2: {
				Name:        "Stage 3",
				Value:       3000,
				PackerIndex: 3,
				ColorIndex: map[int][]int{
					0: {0, 2, 4},
				},
				Color: &rgb.Color{
					Red:        0,
					Green:      255,
					Blue:       0,
					Brightness: 1,
					Hex:        fmt.Sprintf("#%02x%02x%02x", 0, 255, 0),
				},
			},
			3: {
				Name:        "Stage 4",
				Value:       6000,
				PackerIndex: 4,
				ColorIndex: map[int][]int{
					0: {0, 2, 4},
				},
				Color: &rgb.Color{
					Red:        128,
					Green:      0,
					Blue:       128,
					Brightness: 1,
					Hex:        fmt.Sprintf("#%02x%02x%02x", 128, 0, 128),
				},
			},
			4: {
				Name:        "Stage 5",
				Value:       9000,
				PackerIndex: 5,
				ColorIndex: map[int][]int{
					0: {0, 2, 4},
				},
				Color: &rgb.Color{
					Red:        0,
					Green:      255,
					Blue:       255,
					Brightness: 1,
					Hex:        fmt.Sprintf("#%02x%02x%02x", 0, 255, 255),
				},
			},
		}
		deviceProfile.Profile = 1
		deviceProfile.SleepMode = 15
	} else {
		if d.DeviceProfile.BrightnessSlider == nil {
			deviceProfile.BrightnessSlider = &defaultBrightness
			d.DeviceProfile.BrightnessSlider = &defaultBrightness
		} else {
			deviceProfile.BrightnessSlider = d.DeviceProfile.BrightnessSlider
		}
		deviceProfile.Active = d.DeviceProfile.Active
		deviceProfile.Brightness = d.DeviceProfile.Brightness
		deviceProfile.RGBProfile = d.DeviceProfile.RGBProfile
		deviceProfile.Label = d.DeviceProfile.Label
		deviceProfile.Profiles = d.DeviceProfile.Profiles
		deviceProfile.ZoneColors = d.DeviceProfile.ZoneColors
		deviceProfile.Profile = d.DeviceProfile.Profile
		deviceProfile.SleepMode = d.DeviceProfile.SleepMode

		if len(d.DeviceProfile.Path) < 1 {
			deviceProfile.Path = profilePath
			d.DeviceProfile.Path = profilePath
		} else {
			deviceProfile.Path = d.DeviceProfile.Path
		}
	}

	// Convert to JSON
	buffer, err := json.MarshalIndent(deviceProfile, "", "    ")
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to convert to json format")
		return
	}

	// Create profile filename
	file, fileErr := os.Create(deviceProfile.Path)
	if fileErr != nil {
		logger.Log(logger.Fields{"error": err, "location": deviceProfile.Path}).Error("Unable to create new device profile")
		return
	}

	// Write JSON buffer to file
	_, err = file.Write(buffer)
	if err != nil {
		logger.Log(logger.Fields{"error": err, "location": deviceProfile.Path}).Error("Unable to write data")
		return
	}

	// Close file
	err = file.Close()
	if err != nil {
		logger.Log(logger.Fields{"error": err, "location": deviceProfile.Path}).Error("Unable to close file handle")
	}

	d.loadDeviceProfiles() // Reload
}

// loadDeviceProfiles will load custom user profiles
func (d *Device) loadDeviceProfiles() {
	profileList := make(map[string]*DeviceProfile, 0)
	userProfileDirectory := pwd + "/database/profiles/"

	files, err := os.ReadDir(userProfileDirectory)
	if err != nil {
		logger.Log(logger.Fields{"error": err, "location": userProfileDirectory, "serial": d.Serial}).Error("Unable to read content of a folder")
		return
	}

	for _, fi := range files {
		pf := &DeviceProfile{}
		if fi.IsDir() {
			continue // Exclude folders if any
		}

		// Define a full path of filename
		profileLocation := userProfileDirectory + fi.Name()

		// Check if filename has .json extension
		if !common.IsValidExtension(profileLocation, ".json") {
			continue
		}

		fileName := strings.Split(fi.Name(), ".")[0]
		if m, _ := regexp.MatchString("^[a-zA-Z0-9-]+$", fileName); !m {
			continue
		}

		fileSerial := ""
		if strings.Contains(fileName, "-") {
			fileSerial = strings.Split(fileName, "-")[0]
		} else {
			fileSerial = fileName
		}

		if fileSerial != d.Serial {
			continue
		}

		file, err := os.Open(profileLocation)
		if err != nil {
			logger.Log(logger.Fields{"error": err, "serial": d.Serial, "location": profileLocation}).Warn("Unable to load profile")
			continue
		}
		if err = json.NewDecoder(file).Decode(pf); err != nil {
			logger.Log(logger.Fields{"error": err, "serial": d.Serial, "location": profileLocation}).Warn("Unable to decode profile")
			continue
		}
		err = file.Close()
		if err != nil {
			logger.Log(logger.Fields{"location": profileLocation, "serial": d.Serial}).Warn("Failed to close file handle")
		}

		if pf.Serial == d.Serial {
			if fileName == d.Serial {
				profileList["default"] = pf
			} else {
				name := strings.Split(fileName, "-")[1]
				profileList[name] = pf
			}
			logger.Log(logger.Fields{"location": profileLocation, "serial": d.Serial}).Info("Loaded custom user profile")
		}
	}
	d.UserProfiles = profileList
	d.getDeviceProfile()
}

// getDeviceProfile will load persistent device configuration
func (d *Device) getDeviceProfile() {
	if len(d.UserProfiles) == 0 {
		logger.Log(logger.Fields{"serial": d.Serial}).Warn("No profile found for device. Probably initial start")
	} else {
		for _, pf := range d.UserProfiles {
			if pf.Active {
				d.DeviceProfile = pf
			}
		}
	}
}

// initLeds will initialize LED endpoint
func (d *Device) initLeds() {
	_, err := d.transfer(cmdOpenEndpoint, nil)
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to change device mode")
	}
}

// setDeviceColor will activate and set device RGB
func (d *Device) setDeviceColor() {
	buf := make([]byte, d.LEDChannels*3)

	// Reset
	for i := 0; i < d.LEDChannels*3; i++ {
		buf[i] = 0x00
	}
	d.writeColor(buf)

	if d.DeviceProfile == nil {
		logger.Log(logger.Fields{"serial": d.Serial}).Error("Unable to set color. DeviceProfile is null!")
		return
	}

	// DPI
	dpiColor := d.DeviceProfile.Profiles[d.DeviceProfile.Profile].Color
	dpiColor.Brightness = rgb.GetBrightnessValueFloat(*d.DeviceProfile.BrightnessSlider)
	dpiColor = rgb.ModifyBrightness(*dpiColor)

	dpiLeds := d.DeviceProfile.Profiles[d.DeviceProfile.Profile]
	for i := 0; i < len(dpiLeds.ColorIndex); i++ {
		dpiColorIndexRange := dpiLeds.ColorIndex[i]
		for key, dpiColorIndex := range dpiColorIndexRange {
			switch key {
			case 0: // Red
				buf[dpiColorIndex] = byte(dpiColor.Red)
			case 1: // Green
				buf[dpiColorIndex] = byte(dpiColor.Green)
			case 2: // Blue
				buf[dpiColorIndex] = byte(dpiColor.Blue)
			}
		}
	}

	if d.DeviceProfile.RGBProfile == "mouse" {
		for _, zoneColor := range d.DeviceProfile.ZoneColors {
			zoneColor.Color.Brightness = rgb.GetBrightnessValueFloat(*d.DeviceProfile.BrightnessSlider)
			zoneColor.Color = rgb.ModifyBrightness(*zoneColor.Color)
			zoneColorIndexRange := zoneColor.ColorIndex
			for key, zoneColorIndex := range zoneColorIndexRange {
				switch key {
				case 0: // Red
					buf[zoneColorIndex] = byte(zoneColor.Color.Red)
				case 1: // Green
					buf[zoneColorIndex] = byte(zoneColor.Color.Green)
				case 2: // Blue
					buf[zoneColorIndex] = byte(zoneColor.Color.Blue)
				}
			}
		}
		d.writeColor(buf)
		return
	}

	if d.DeviceProfile.RGBProfile == "static" {
		profile := d.GetRgbProfile("static")
		if profile == nil {
			return
		}

		profile.StartColor.Brightness = rgb.GetBrightnessValueFloat(*d.DeviceProfile.BrightnessSlider)
		profileColor := rgb.ModifyBrightness(profile.StartColor)
		for _, zoneColor := range d.DeviceProfile.ZoneColors {
			zoneColorIndexRange := zoneColor.ColorIndex
			for key, zoneColorIndex := range zoneColorIndexRange {
				switch key {
				case 0: // Red
					buf[zoneColorIndex] = byte(profileColor.Red)
				case 1: // Green
					buf[zoneColorIndex] = byte(profileColor.Green)
				case 2: // Blue
					buf[zoneColorIndex] = byte(profileColor.Blue)
				}
			}
		}
		d.writeColor(buf)
		return
	}

	go func(lightChannels int) {
		lock := sync.Mutex{}
		startTime := time.Now()
		reverse := false
		counterColorpulse := 0
		counterFlickering := 0
		counterColorshift := 0
		counterCircleshift := 0
		counterCircle := 0
		counterColorwarp := 0
		counterSpinner := 0
		counterCpuTemp := 0
		counterGpuTemp := 0
		var temperatureKeys *rgb.Color
		colorwarpGeneratedReverse := false
		d.activeRgb = rgb.Exit()

		// Generate random colors
		d.activeRgb.RGBStartColor = rgb.GenerateRandomColor(1)
		d.activeRgb.RGBEndColor = rgb.GenerateRandomColor(1)

		hue := 1
		wavePosition := 0.0
		for {
			select {
			case <-d.activeRgb.Exit:
				return
			default:
				buff := make([]byte, 0)
				rgbCustomColor := true
				profile := d.GetRgbProfile(d.DeviceProfile.RGBProfile)
				if profile == nil {
					for i := 0; i < d.ChangeableLedChannels*3; i++ {
						buff = append(buff, []byte{0, 0, 0}...)
					}
					logger.Log(logger.Fields{"profile": d.DeviceProfile.RGBProfile, "serial": d.Serial}).Warn("No such RGB profile found")
					continue
				}
				rgbModeSpeed := common.FClamp(profile.Speed, 0.1, 10)
				// Check if we have custom colors
				if (rgb.Color{}) == profile.StartColor || (rgb.Color{}) == profile.EndColor {
					rgbCustomColor = false
				}

				r := rgb.New(
					d.ChangeableLedChannels,
					rgbModeSpeed,
					nil,
					nil,
					profile.Brightness,
					common.Clamp(profile.Smoothness, 1, 100),
					time.Duration(rgbModeSpeed)*time.Second,
					rgbCustomColor,
				)

				if rgbCustomColor {
					r.RGBStartColor = &profile.StartColor
					r.RGBEndColor = &profile.EndColor
				} else {
					r.RGBStartColor = d.activeRgb.RGBStartColor
					r.RGBEndColor = d.activeRgb.RGBEndColor
				}

				// Brightness
				r.RGBBrightness = rgb.GetBrightnessValueFloat(*d.DeviceProfile.BrightnessSlider)
				r.RGBStartColor.Brightness = r.RGBBrightness
				r.RGBEndColor.Brightness = r.RGBBrightness

				switch d.DeviceProfile.RGBProfile {
				case "off":
					{
						for n := 0; n < d.ChangeableLedChannels; n++ {
							buff = append(buff, []byte{0, 0, 0}...)
						}
					}
				case "rainbow":
					{
						r.Rainbow(startTime)
						buff = append(buff, r.Output...)
					}
				case "watercolor":
					{
						r.Watercolor(startTime)
						buff = append(buff, r.Output...)
					}
				case "cpu-temperature":
					{
						lock.Lock()
						counterCpuTemp++
						if counterCpuTemp >= r.Smoothness {
							counterCpuTemp = 0
						}

						if temperatureKeys == nil {
							temperatureKeys = r.RGBStartColor
						}

						r.MinTemp = profile.MinTemp
						r.MaxTemp = profile.MaxTemp
						res := r.Temperature(float64(d.CpuTemp), counterCpuTemp, temperatureKeys)
						temperatureKeys = res
						lock.Unlock()
						buff = append(buff, r.Output...)
					}
				case "gpu-temperature":
					{
						lock.Lock()
						counterGpuTemp++
						if counterGpuTemp >= r.Smoothness {
							counterGpuTemp = 0
						}

						if temperatureKeys == nil {
							temperatureKeys = r.RGBStartColor
						}

						r.MinTemp = profile.MinTemp
						r.MaxTemp = profile.MaxTemp
						res := r.Temperature(float64(d.GpuTemp), counterGpuTemp, temperatureKeys)
						temperatureKeys = res
						lock.Unlock()
						buff = append(buff, r.Output...)
					}
				case "colorpulse":
					{
						lock.Lock()
						counterColorpulse++
						if counterColorpulse >= r.Smoothness {
							counterColorpulse = 0
						}

						r.Colorpulse(counterColorpulse)
						lock.Unlock()
						buff = append(buff, r.Output...)
					}
				case "static":
					{
						r.Static()
						buff = append(buff, r.Output...)
					}
				case "rotator":
					{
						r.Rotator(hue)
						buff = append(buff, r.Output...)
					}
				case "wave":
					{
						r.Wave(wavePosition)
						buff = append(buff, r.Output...)
					}
				case "storm":
					{
						r.Storm()
						buff = append(buff, r.Output...)
					}
				case "flickering":
					{
						lock.Lock()
						if counterFlickering >= r.Smoothness {
							counterFlickering = 0
						} else {
							counterFlickering++
						}

						r.Flickering(counterFlickering)
						lock.Unlock()
						buff = append(buff, r.Output...)
					}
				case "colorshift":
					{
						lock.Lock()
						if counterColorshift >= r.Smoothness && !reverse {
							counterColorshift = 0
							reverse = true
						} else if counterColorshift >= r.Smoothness && reverse {
							counterColorshift = 0
							reverse = false
						}

						r.Colorshift(counterColorshift, reverse)
						counterColorshift++
						lock.Unlock()
						buff = append(buff, r.Output...)
					}
				case "circleshift":
					{
						lock.Lock()
						if counterCircleshift >= lightChannels {
							counterCircleshift = 0
						} else {
							counterCircleshift++
						}

						r.Circle(counterCircleshift)
						lock.Unlock()
						buff = append(buff, r.Output...)
					}
				case "circle":
					{
						lock.Lock()
						if counterCircle >= lightChannels {
							counterCircle = 0
						} else {
							counterCircle++
						}

						r.Circle(counterCircle)
						lock.Unlock()
						buff = append(buff, r.Output...)
					}
				case "spinner":
					{
						lock.Lock()
						if counterSpinner >= lightChannels {
							counterSpinner = 0
						} else {
							counterSpinner++
						}
						r.Spinner(counterSpinner)
						lock.Unlock()
						buff = append(buff, r.Output...)
					}
				case "colorwarp":
					{
						lock.Lock()
						if counterColorwarp >= r.Smoothness {
							if !colorwarpGeneratedReverse {
								colorwarpGeneratedReverse = true
								d.activeRgb.RGBStartColor = d.activeRgb.RGBEndColor
								d.activeRgb.RGBEndColor = rgb.GenerateRandomColor(r.RGBBrightness)
							}
							counterColorwarp = 0
						} else if counterColorwarp == 0 && colorwarpGeneratedReverse == true {
							colorwarpGeneratedReverse = false
						} else {
							counterColorwarp++
						}

						r.Colorwarp(counterColorwarp, d.activeRgb.RGBStartColor, d.activeRgb.RGBEndColor)
						lock.Unlock()
						buff = append(buff, r.Output...)
					}
				}
				m := 0
				for _, zoneColor := range d.DeviceProfile.ZoneColors {
					zoneColorIndexRange := zoneColor.ColorIndex
					for _, zoneColorIndex := range zoneColorIndexRange {
						buf[zoneColorIndex] = buff[m]
						m++
					}
				}

				d.writeColor(buf)
				time.Sleep(40 * time.Millisecond)
				hue++
				wavePosition += 0.2
			}
		}
	}(d.ChangeableLedChannels)
}

func (d *Device) ModifyDpi() {
	if d.DeviceProfile.Profile >= 4 {
		d.DeviceProfile.Profile = 0
	} else {
		d.DeviceProfile.Profile++
	}
	d.saveDeviceProfile()
	d.toggleDPI()
}

// toggleDPI will change DPI mode
func (d *Device) toggleDPI() {
	if d.DeviceProfile != nil {
		profile := d.DeviceProfile.Profiles[d.DeviceProfile.Profile]
		value := profile.Value

		// Send DPI packet
		if value < uint16(minDpiValue) {
			value = uint16(minDpiValue)
		}
		if value > uint16(maxDpiValue) {
			value = uint16(maxDpiValue)
		}

		buf := make([]byte, 2)
		binary.LittleEndian.PutUint16(buf[0:2], value)
		for i := 0; i <= 1; i++ {
			_, err := d.transfer(cmdSetDpi[i], buf)
			if err != nil {
				logger.Log(logger.Fields{"error": err, "vendorId": d.VendorId}).Error("Unable to set dpi")
			}
		}

		if d.activeRgb != nil {
			d.activeRgb.Exit <- true // Exit current RGB mode
			d.activeRgb = nil
		}
		d.setDeviceColor() // Restart RGB
	}
}

// keepAlive will keep a device alive
func (d *Device) keepAlive() {
	_, err := d.transferDevice([]byte{0x12}, nil)
	if err != nil {
		logger.Log(logger.Fields{"error": err, "serial": d.Serial}).Error("Unable to write to a device")
	}
}

// setAutoRefresh will refresh device data
func (d *Device) setKeepAlive() {
	timerKeepAlive = time.NewTicker(time.Duration(deviceKeepAlive) * time.Millisecond)
	keepAliveChan = make(chan bool)
	go func() {
		for {
			select {
			case <-timerKeepAlive.C:
				d.keepAlive()
			case <-keepAliveChan:
				timerKeepAlive.Stop()
				return
			}
		}
	}()
}

// writeColor will write data to the device with a specific endpoint.
func (d *Device) writeColor(data []byte) {
	buffer := make([]byte, len(data)+headerWriteSize)
	binary.LittleEndian.PutUint16(buffer[0:2], uint16(len(data)))
	copy(buffer[headerWriteSize:], data)

	_, err := d.transfer(cmdWriteColor, buffer)
	if err != nil {
		logger.Log(logger.Fields{"error": err, "serial": d.Serial}).Error("Unable to write to color endpoint")
	}
}

// transfer will send data to a device and retrieve device output
func (d *Device) transfer(endpoint, buffer []byte) ([]byte, error) {
	// Packet control, mandatory for this device
	mutex.Lock()
	defer mutex.Unlock()

	// Create write buffer
	bufferW := make([]byte, bufferSizeWrite)
	bufferW[1] = 0x08
	endpointHeaderPosition := bufferW[headerSize : headerSize+len(endpoint)]
	copy(endpointHeaderPosition, endpoint)
	if len(buffer) > 0 {
		copy(bufferW[headerSize+len(endpoint):headerSize+len(endpoint)+len(buffer)], buffer)
	}

	// Create read buffer
	bufferR := make([]byte, bufferSize)

	// Send command to a device
	if _, err := d.dev.Write(bufferW); err != nil {
		logger.Log(logger.Fields{"error": err, "serial": d.Serial}).Error("Unable to write to a device")
		return nil, err
	}

	// Get data from a device
	if _, err := d.dev.Read(bufferR); err != nil {
		logger.Log(logger.Fields{"error": err, "serial": d.Serial}).Error("Unable to read data from device")
		return nil, err
	}
	return bufferR, nil
}

// transfer will send data to a device and retrieve device output
func (d *Device) transferDevice(endpoint, buffer []byte) ([]byte, error) {
	// Packet control, mandatory for this device
	mutex.Lock()
	defer mutex.Unlock()

	// Create write buffer
	bufferW := make([]byte, bufferSizeWrite)
	bufferW[1] = 0x08
	endpointHeaderPosition := bufferW[headerSize : headerSize+len(endpoint)]
	copy(endpointHeaderPosition, endpoint)
	if len(buffer) > 0 {
		copy(bufferW[headerSize+len(endpoint):headerSize+len(endpoint)+len(buffer)], buffer)
	}

	// Create read buffer
	bufferR := make([]byte, bufferSize)

	// Send command to a device
	if _, err := d.dev.Write(bufferW); err != nil {
		logger.Log(logger.Fields{"error": err, "serial": d.Serial}).Error("Unable to write to a device")
		return nil, err
	}

	// Get data from a device
	if _, err := d.dev.ReadWithTimeout(bufferR, 50*time.Millisecond); err != nil {
		logger.Log(logger.Fields{"error": err, "serial": d.Serial}).Error("Unable to read data from device")
		return nil, err
	}
	return bufferR, nil
}

// controlListener will listen for events from the control buttons
func (d *Device) controlListener() {
	go func() {
		enum := hid.EnumFunc(func(info *hid.DeviceInfo) error {
			if info.InterfaceNbr == 2 {
				listener, err := hid.OpenPath(info.Path)
				if err != nil {
					return err
				}
				d.listener = listener
			}
			return nil
		})

		err := hid.Enumerate(d.VendorId, d.ProductId, enum)
		if err != nil {
			logger.Log(logger.Fields{"error": err, "vendorId": d.VendorId}).Error("Unable to enumerate devices")
		}

		// Listen loop
		data := make([]byte, bufferSize)
		for {
			if d.listener == nil {
				break
			}

			_, err = d.listener.Read(data)
			if err != nil {
				logger.Log(logger.Fields{"error": err, "serial": d.Serial}).Error("Error reading data")
				break
			}

			if data[1] == 0x02 {
				if data[2] == 0x80 {
					d.ModifyDpi()
				} else if data[2] == 0x10 {
					// Upper side button - Left side
				} else if data[2] == 0x08 {
					// Bottom side button - Left side
				} else if data[2] == 0x40 {
					// Upper side button - Right side
				} else if data[2] == 0x20 {
					// Bottom side button - Right side
				}
			}
		}
	}()
}
