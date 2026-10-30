package usbip

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Standard descriptor types.
const (
	descDevice    = 1
	descConfig    = 2
	descInterface = 4
	descEndpoint  = 5
)

// Device is a remote device as described by the browser's Hello.
type Device struct {
	Hello
	BusID string

	VendorID, ProductID uint16
	Speed               uint32
	// maxPacket is wMaxPacketSize by endpoint address, over all configs
	// and alternate settings (the largest wins). Used for URB_ZERO_PACKET.
	maxPacket map[uint8]int
}

// NewDevice validates the descriptors and derives what the USB/IP side
// needs from them.
func NewDevice(h *Hello, busID string) (*Device, error) {
	dd := h.Device
	if len(dd) < 18 || dd[0] < 18 || dd[1] != descDevice {
		return nil, errors.New("usbip: invalid device descriptor")
	}
	d := &Device{
		Hello:     *h,
		BusID:     busID,
		VendorID:  binary.LittleEndian.Uint16(dd[8:]),
		ProductID: binary.LittleEndian.Uint16(dd[10:]),
		maxPacket: map[uint8]int{},
	}
	for i, c := range h.Configs {
		if len(c) < 9 || c[1] != descConfig {
			return nil, fmt.Errorf("usbip: invalid configuration descriptor %d", i)
		}
		for _, ep := range endpoints(c) {
			if ep.maxPacket > d.maxPacket[ep.addr] {
				d.maxPacket[ep.addr] = ep.maxPacket
			}
		}
	}
	d.Speed = guessSpeed(d)
	return d, nil
}

type endpoint struct {
	addr      uint8
	attrs     uint8 // bits 0-1: transfer type
	maxPacket int   // bytes per packet, without the HS multiplier bits
}

// walk calls fn for each descriptor in a configuration blob.
func walk(cfg []byte, fn func(d []byte)) {
	for len(cfg) >= 2 {
		n := int(cfg[0])
		if n < 2 || n > len(cfg) {
			return
		}
		fn(cfg[:n])
		cfg = cfg[n:]
	}
}

func endpoints(cfg []byte) []endpoint {
	var out []endpoint
	walk(cfg, func(d []byte) {
		if d[1] == descEndpoint && len(d) >= 7 {
			out = append(out, endpoint{d[2], d[3], int(binary.LittleEndian.Uint16(d[4:]) & 0x7ff)})
		}
	})
	return out
}

// interfaces lists the alternate-setting-0 interfaces of a configuration.
func interfaces(cfg []byte) []Interface {
	var out []Interface
	walk(cfg, func(d []byte) {
		if d[1] == descInterface && len(d) >= 9 && d[3] == 0 {
			out = append(out, Interface{d[5], d[6], d[7]})
		}
	})
	return out
}

// guessSpeed infers the bus speed, which WebUSB doesn't expose. The
// device descriptors were read at the device's actual speed, so the
// endpoint packet sizes betray it: ep0 of 512 bytes (bMaxPacketSize0 = 9)
// means SuperSpeed, and any packet larger than full speed allows (64,
// or 1023 for iso) means high speed.
func guessSpeed(d *Device) uint32 {
	dd := d.Device
	bcd := binary.LittleEndian.Uint16(dd[2:])
	if bcd >= 0x0300 && dd[7] == 9 {
		return SpeedSuper
	}
	for _, c := range d.Configs {
		for _, ep := range endpoints(c) {
			switch ep.attrs & 3 {
			case 1: // iso
				if ep.maxPacket > 1023 {
					return SpeedHigh
				}
			default:
				if ep.maxPacket > 64 {
					return SpeedHigh
				}
			}
		}
	}
	// Low speed is only distinguishable by an 8-byte ep0 on a USB 1.x
	// device with no bulk endpoints; full speed treats it the same way.
	return SpeedFull
}

// config returns the configuration descriptor whose bConfigurationValue
// is v, or nil.
func (d *Device) config(v uint8) []byte {
	for _, c := range d.Configs {
		if c[5] == v {
			return c
		}
	}
	return nil
}

// Info is the USB/IP export record for this device.
func (d *Device) Info() DeviceInfo {
	dd := d.Device
	info := DeviceInfo{
		Path:               "/urdp/webusb/" + d.BusID,
		BusID:              d.BusID,
		BusNum:             1,
		DevNum:             1,
		Speed:              d.Speed,
		IDVendor:           d.VendorID,
		IDProduct:          d.ProductID,
		BCDDevice:          binary.LittleEndian.Uint16(dd[12:]),
		DeviceClass:        dd[4],
		DeviceSubClass:     dd[5],
		DeviceProtocol:     dd[6],
		ConfigurationValue: d.ActiveConfig,
		NumConfigurations:  dd[17],
	}
	if c := d.config(d.ActiveConfig); c != nil {
		info.Interfaces = interfaces(c)
		info.NumInterfaces = c[4]
	}
	return info
}
