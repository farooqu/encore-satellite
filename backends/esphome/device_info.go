// Package esphome adapts Encore devices to the ESPHome native API.
package esphome

import (
	"cmp"
	"strings"

	device "github.com/ygelfand/go-esphome-device"
)

// CompatibilityVersion is the ESPHome version for both discovery and device info.
const CompatibilityVersion = device.DefaultESPHomeVersion

// DeviceInfo formats the hardware labels and installed version for Home Assistant.
// Name and MAC identify the unit and are passed through unchanged.
func DeviceInfo(info device.Info) device.Info {
	label := func(value string) string {
		value = strings.Join(strings.Fields(value), " ")
		if strings.EqualFold(value, "unknown") {
			return ""
		}
		return value
	}
	manufacturer, model := label(info.Manufacturer), label(info.Model)
	info.Manufacturer, info.Model = cmp.Or(manufacturer, "Unknown"), cmp.Or(model, "Unknown")
	hardware := cmp.Or(strings.TrimSpace(manufacturer+" "+model), "Unknown")
	// Home Assistant displays the first two dot-separated parts as manufacturer
	// and model. Keep periods inside the hardware label from splitting it.
	info.ProjectName = "Encore Satellite." + strings.ReplaceAll(hardware, ".", "․")
	info.Version = strings.TrimSpace(info.Version)
	if info.Version == "" {
		info.Version = "unknown"
	}
	info.ESPHomeVersion = CompatibilityVersion
	return info
}
