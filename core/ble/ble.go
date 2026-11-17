// Package ble defines the shared Bluetooth scanner and GATT contracts.
// It has no dependency on ESPHome, Linux, audio or Wi-Fi provisioning.
package ble

import "context"

// AddressTypeUnknown is explicit missing metadata, not a public/random guess.
// It is usable only by non-connectable consumers; never use it for GATT.
const AddressTypeUnknown uint8 = 0xff

// Advertisement preserves the controller's raw AD structures. Address is the
// printed MAC as an integer (12:34:56:78:9A:BC is 0x123456789abc), not HCI order.
type Advertisement struct {
	Address     uint64
	AddressType uint8
	RSSI        int8
	Data        []byte
}

// Scanner requires a positively identified BLE-capable controller and an
// initialized host bridge before being offered to a protocol backend. The host
// may gate temporary permission/radio availability separately. Scan blocks until an
// error or cancellation, disables scanning before returning, and never calls
// found after returning. Only one Scan may run at once. A nil return from ready
// means the peer was notified; a non-nil return must stop the scan.
//
// Active scanning transmits scan requests; it is NOT a GATT connection. Callers
// must make found nonblocking and copy Data before retaining it. Neither mute
// state nor microphone samples belong to this interface. The host decides when
// to cancel scanning for privacy, provisioning or radio/audio contention.
type Scanner interface {
	Scan(ctx context.Context, active bool, ready func() error, found func(Advertisement)) error
}

// Service/Characteristic handles are ATT handles (including characteristic
// VALUE handles), stable across proxies and used by HA's remote service cache.
type Service struct {
	Handle          uint16
	UUID            string
	Characteristics []Characteristic
}
type Characteristic struct {
	Handle      uint16
	UUID        string
	Flags       []string
	Descriptors []Descriptor
}
type Descriptor struct {
	Handle uint16
	UUID   string
}

// Central is an initialized GATT client. Pair supports Just Works bonding,
// not invented PINs or silently confirmed numeric comparison. Hosts without
// bonding implement Pairing as false. Close must disconnect all owned links.
type Central interface {
	Connect(context.Context, string, bool) (int, error)
	Disconnect(context.Context, string) error
	Services(string) ([]Service, error)
	Read(context.Context, string, uint16) ([]byte, error)
	Write(context.Context, string, uint16, []byte, bool) error
	Notify(context.Context, string, uint16, bool) error
	Pair(context.Context, string) error
	Unpair(context.Context, string) error
	Close() error
}
