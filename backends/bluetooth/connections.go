package bluetooth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"

	"github.com/farooqu/encore-satellite/core/ble"
)

const slots = 3
const gattError = 0x85
const noResources = 0x80

type request struct {
	conn       *esphome.Conn
	msg        proto.Message
	generation uint64
}
type event struct {
	address string
	handle  uint16
	data    []byte
	lost    bool
}

// One bounded worker serializes GATT operations, including pairing. A remote
// peer cannot create a goroutine per request, run concurrent bonds, or free a
// slot before disconnect has finished. Cancelling a lease fences queued ops,
// in-flight connects and notification callbacks before a new owner starts.
type connections struct {
	b          *Proxy
	mu         sync.Mutex
	owner      *esphome.Conn
	subscriber *esphome.Conn
	cancel     context.CancelFunc
	opCancel   context.CancelFunc
	opAddress  uint64
	generation uint64
	requests   chan request
}

func newConnections(b *Proxy) *connections {
	return &connections{b: b, requests: make(chan request, 32)}
}

func handles(msg proto.Message) bool {
	switch msg.(type) {
	case *api.SubscribeBluetoothConnectionsFreeRequest, *api.BluetoothDeviceRequest,
		*api.BluetoothGATTGetServicesRequest, *api.BluetoothGATTReadRequest,
		*api.BluetoothGATTWriteRequest, *api.BluetoothGATTReadDescriptorRequest,
		*api.BluetoothGATTWriteDescriptorRequest, *api.BluetoothGATTNotifyRequest:
		return true
	}
	return false
}

func refuse(c *esphome.Conn, msg proto.Message) error {
	switch m := msg.(type) {
	case *api.SubscribeBluetoothConnectionsFreeRequest:
		return c.Send(&api.BluetoothConnectionsFreeResponse{})
	case *api.BluetoothDeviceRequest:
		switch m.RequestType {
		case api.BluetoothDeviceRequestType_BLUETOOTH_DEVICE_REQUEST_TYPE_PAIR:
			return c.Send(&api.BluetoothDevicePairingResponse{Address: m.Address, Error: noResources})
		case api.BluetoothDeviceRequestType_BLUETOOTH_DEVICE_REQUEST_TYPE_UNPAIR:
			return c.Send(&api.BluetoothDeviceUnpairingResponse{Address: m.Address, Error: noResources})
		case api.BluetoothDeviceRequestType_BLUETOOTH_DEVICE_REQUEST_TYPE_CLEAR_CACHE:
			return c.Send(&api.BluetoothDeviceClearCacheResponse{Address: m.Address, Error: 6})
		}
		return c.Send(&api.BluetoothDeviceConnectionResponse{Address: m.Address, Error: noResources})
	case interface {
		GetAddress() uint64
		GetHandle() uint32
	}:
		return c.Send(&api.BluetoothGATTErrorResponse{Address: m.GetAddress(), Handle: m.GetHandle(), Error: gattError})
	case *api.BluetoothGATTGetServicesRequest:
		return c.Send(&api.BluetoothGATTErrorResponse{Address: m.Address, Error: gattError})
	}
	return nil
}

func (l *connections) handle(c *esphome.Conn, msg proto.Message) error {
	if _, ok := msg.(*api.SubscribeBluetoothConnectionsFreeRequest); ok {
		l.mu.Lock()
		l.subscriber = c
		l.mu.Unlock()
	}
	if !l.b.enabledNow() || l.b.central == nil {
		return refuse(c, msg)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.owner != nil && l.owner != c {
		return refuse(c, msg)
	}
	if l.owner == nil {
		l.owner = c
	}
	if m, ok := msg.(*api.BluetoothDeviceRequest); ok && m.RequestType == api.BluetoothDeviceRequestType_BLUETOOTH_DEVICE_REQUEST_TYPE_DISCONNECT && l.opCancel != nil && l.opAddress == m.Address {
		l.opCancel() // Do not queue a disconnect behind the connect it cancels.
	}
	select {
	case l.requests <- request{c, proto.Clone(msg), l.generation}:
		return nil
	default:
		return refuse(c, msg)
	}
}

func (l *connections) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.generation++
	l.owner = nil
	if l.cancel != nil {
		l.cancel()
	}
}

func (l *connections) closed(c *esphome.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.subscriber == c {
		l.subscriber = nil
	}
	if l.owner == c {
		l.generation++
		l.owner = nil
		if l.cancel != nil {
			l.cancel()
		}
	}
}

func (l *connections) refresh() {
	l.mu.Lock()
	c := l.subscriber
	l.mu.Unlock()
	if c != nil {
		_ = l.handle(c, &api.SubscribeBluetoothConnectionsFreeRequest{})
	}
}

// Invalidate only this lease: late callbacks must never cancel a newer owner.
func (l *connections) expire(first request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.generation == first.generation && l.owner == first.conn {
		l.generation++
		l.owner = nil
		if l.cancel != nil {
			l.cancel()
		}
	}
}

func (l *connections) run(ctx context.Context) {
	var pending *request
	for {
		if ctx.Err() != nil {
			return
		}
		var first request
		if pending != nil {
			first, pending = *pending, nil
		} else {
			select {
			case <-ctx.Done():
				return
			case first = <-l.requests:
			}
		}
		l.mu.Lock()
		if first.generation != l.generation || first.conn != l.owner {
			l.mu.Unlock()
			_ = refuse(first.conn, first.msg)
			continue
		}
		lease, cancel := context.WithCancel(ctx)
		l.cancel = cancel
		l.mu.Unlock()
		pending = l.lease(lease, cancel, first)
		cancel()
	}
}

func (l *connections) lease(ctx context.Context, cancel context.CancelFunc, first request) *request {
	c := first.conn
	defer l.expire(first)
	events := make(chan event, 64)
	push := func(e event) {
		if ctx.Err() != nil {
			return
		}
		select {
		case events <- e:
		default:
			l.expire(first)
			cancel()
		}
	}
	ct, err := l.b.central(func(a string, h uint16, d []byte) {
		if len(d) > 512 {
			l.expire(first)
			return
		}
		push(event{address: a, handle: h, data: append([]byte(nil), d...)})
	}, func(a string) { push(event{address: a, lost: true}) })
	if err != nil {
		_ = refuse(c, first.msg)
		return nil
	}
	peers := map[uint64]uint32{}
	defer func() {
		cancel()
		_ = ct.Close()
		for a := range peers {
			_ = c.Send(&api.BluetoothDeviceConnectionResponse{Address: a})
		}
		_ = c.Send(&api.BluetoothConnectionsFreeResponse{})
	}()
	free := func() error {
		r := &api.BluetoothConnectionsFreeResponse{Free: uint32(slots - len(peers)), Limit: slots}
		for a := range peers {
			r.Allocated = append(r.Allocated, a)
		}
		return c.Send(r)
	}
	process := func(r request) error {
		if r.generation != first.generation || r.conn != c || ctx.Err() != nil || !l.b.enabledNow() {
			return refuse(r.conn, r.msg)
		}
		if _, ok := r.msg.(*api.SubscribeBluetoothConnectionsFreeRequest); ok {
			return free()
		}
		opCtx, done := context.WithTimeout(ctx, 30*time.Second)
		defer done()
		l.mu.Lock()
		l.opAddress = r.msg.(interface{ GetAddress() uint64 }).GetAddress()
		l.opCancel = done
		l.mu.Unlock()
		defer func() {
			l.mu.Lock()
			l.opCancel = nil
			l.mu.Unlock()
		}()
		err := l.operation(opCtx, ct, c, peers, r.msg)
		if err == nil {
			return free()
		}
		return err
	}
	if process(first) != nil {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case r := <-l.requests:
			if r.generation != first.generation || r.conn != c {
				// A rapid OFF/ON or reconnect can queue a fresh lease while
				// this one unwinds. Preserve it until cleanup has finished.
				return &r
			}
			if err := process(r); err != nil {
				_ = c.Close()
				return nil
			}
		case e := <-events:
			if ctx.Err() != nil {
				continue
			}
			a, err := strconv.ParseUint(strings.ReplaceAll(e.address, ":", ""), 16, 48)
			if err != nil {
				continue
			}
			if _, ok := peers[a]; !ok {
				continue
			}
			var reply proto.Message = &api.BluetoothGATTNotifyDataResponse{Address: a, Handle: uint32(e.handle), Data: e.data}
			if e.lost {
				delete(peers, a)
				reply = &api.BluetoothDeviceConnectionResponse{Address: a}
			}
			if err := c.Send(reply); err != nil {
				return nil
			}
			if e.lost {
				if free() != nil {
					return nil
				}
			}
		}
	}
}

func (l *connections) operation(ctx context.Context, ct ble.Central, c *esphome.Conn, peers map[uint64]uint32, msg proto.Message) error {
	a := msg.(interface{ GetAddress() uint64 }).GetAddress()
	if a > 0xffffffffffff {
		return refuse(c, msg)
	}
	addr := fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", byte(a>>40), byte(a>>32), byte(a>>24), byte(a>>16), byte(a>>8), byte(a))
	if m, ok := msg.(*api.BluetoothDeviceRequest); ok {
		switch m.RequestType {
		case api.BluetoothDeviceRequestType_BLUETOOTH_DEVICE_REQUEST_TYPE_CONNECT_V3_WITH_CACHE, api.BluetoothDeviceRequestType_BLUETOOTH_DEVICE_REQUEST_TYPE_CONNECT_V3_WITHOUT_CACHE:
			if mtu, ok := peers[a]; ok {
				return c.Send(&api.BluetoothDeviceConnectionResponse{Address: a, Connected: true, Mtu: mtu})
			}
			if len(peers) >= slots || m.AddressType > 1 {
				return refuse(c, msg)
			}
			mtu, err := ct.Connect(ctx, addr, m.HasAddressType && m.AddressType == 1)
			if err != nil || ctx.Err() != nil {
				cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
				_ = ct.Disconnect(cleanup, addr)
				done()
				return c.Send(&api.BluetoothDeviceConnectionResponse{Address: a, Error: gattError})
			}
			peers[a] = uint32(mtu)
			return c.Send(&api.BluetoothDeviceConnectionResponse{Address: a, Connected: true, Mtu: uint32(mtu)})
		case api.BluetoothDeviceRequestType_BLUETOOTH_DEVICE_REQUEST_TYPE_DISCONNECT:
			err := ct.Disconnect(ctx, addr)
			if err != nil {
				return c.Send(&api.BluetoothDeviceConnectionResponse{Address: a, Connected: true, Mtu: peers[a], Error: gattError})
			}
			delete(peers, a)
			return c.Send(&api.BluetoothDeviceConnectionResponse{Address: a})
		case api.BluetoothDeviceRequestType_BLUETOOTH_DEVICE_REQUEST_TYPE_PAIR, api.BluetoothDeviceRequestType_BLUETOOTH_DEVICE_REQUEST_TYPE_UNPAIR:
			if !l.b.pairing {
				return refuse(c, msg)
			}
			pair := m.RequestType == api.BluetoothDeviceRequestType_BLUETOOTH_DEVICE_REQUEST_TYPE_PAIR
			var err error
			if pair {
				if _, ok := peers[a]; !ok {
					return refuse(c, msg)
				}
				err = ct.Pair(ctx, addr)
			} else {
				err = ct.Unpair(ctx, addr)
			}
			var code int32
			if err != nil {
				code = gattError
			}
			if pair {
				return c.Send(&api.BluetoothDevicePairingResponse{Address: a, Paired: err == nil, Error: code})
			}
			// HA waits for either this result OR a disconnect. A disconnect
			// first would turn a successful unpair into a client-side error.
			if sendErr := c.Send(&api.BluetoothDeviceUnpairingResponse{Address: a, Success: err == nil, Error: code}); sendErr != nil {
				return sendErr
			}
			if err == nil {
				delete(peers, a)
				return c.Send(&api.BluetoothDeviceConnectionResponse{Address: a})
			}
			return nil
		default:
			return refuse(c, msg)
		}
	}
	if _, ok := peers[a]; !ok {
		return refuse(c, msg)
	}
	var handle uint32
	if m, ok := msg.(interface{ GetHandle() uint32 }); ok {
		handle = m.GetHandle()
		if handle == 0 || handle > 65535 {
			return refuse(c, msg)
		}
	}
	var err error
	var reply proto.Message
	switch m := msg.(type) {
	case *api.BluetoothGATTGetServicesRequest:
		var services []ble.Service
		services, err = ct.Services(addr)
		if err == nil {
			for _, s := range services {
				if err := c.Send(&api.BluetoothGATTGetServicesResponse{Address: a, Services: []*api.BluetoothGATTService{gattService(s)}}); err != nil {
					return err
				}
			}
			reply = &api.BluetoothGATTGetServicesDoneResponse{Address: a}
		}
	case *api.BluetoothGATTReadRequest, *api.BluetoothGATTReadDescriptorRequest:
		var data []byte
		data, err = ct.Read(ctx, addr, uint16(handle))
		reply = &api.BluetoothGATTReadResponse{Address: a, Handle: handle, Data: data}
	case *api.BluetoothGATTWriteRequest:
		if len(m.Data) > 512 {
			err = errors.New("GATT value too long")
		} else {
			err = ct.Write(ctx, addr, uint16(handle), m.Data, m.Response)
		}
		reply = &api.BluetoothGATTWriteResponse{Address: a, Handle: handle}
	case *api.BluetoothGATTWriteDescriptorRequest:
		if len(m.Data) > 512 {
			err = errors.New("GATT value too long")
		} else {
			err = ct.Write(ctx, addr, uint16(handle), m.Data, true)
		}
		reply = &api.BluetoothGATTWriteResponse{Address: a, Handle: handle}
	case *api.BluetoothGATTNotifyRequest:
		err = ct.Notify(ctx, addr, uint16(handle), m.Enable)
		reply = &api.BluetoothGATTNotifyResponse{Address: a, Handle: handle}
	}
	if err != nil {
		return c.Send(&api.BluetoothGATTErrorResponse{Address: a, Handle: handle, Error: gattError})
	}
	if ctx.Err() != nil {
		return nil
	}
	return c.Send(reply)
}

// UUID/properties conversion adapted from TECHO5's bluetooth/connect.go at
// 2b091439765d5f881de4b89255b8cf0637d80cba; licenses/Techo5-MIT.txt.
func splitUUID(u string) []uint64 {
	h := strings.ReplaceAll(u, "-", "")
	if len(h) != 32 {
		return nil
	}
	hi, e1 := strconv.ParseUint(h[:16], 16, 64)
	lo, e2 := strconv.ParseUint(h[16:], 16, 64)
	if e1 != nil || e2 != nil {
		return nil
	}
	return []uint64{hi, lo}
}
func gattService(s ble.Service) *api.BluetoothGATTService {
	r := &api.BluetoothGATTService{Uuid: splitUUID(s.UUID), Handle: uint32(s.Handle)}
	flags := map[string]uint32{"broadcast": 1, "read": 2, "write-without-response": 4, "write": 8, "notify": 16, "indicate": 32, "authenticated-signed-writes": 64, "extended-properties": 128}
	for _, c := range s.Characteristics {
		ch := &api.BluetoothGATTCharacteristic{Uuid: splitUUID(c.UUID), Handle: uint32(c.Handle)}
		for _, f := range c.Flags {
			ch.Properties |= flags[f]
		}
		for _, d := range c.Descriptors {
			ch.Descriptors = append(ch.Descriptors, &api.BluetoothGATTDescriptor{Uuid: splitUUID(d.UUID), Handle: uint32(d.Handle)})
		}
		r.Characteristics = append(r.Characteristics, ch)
	}
	return r
}
