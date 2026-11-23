package improv

import (
	"context"
	"errors"
	"time"
)

// Options deliberately has no proxy setting. Join must observe cancellation,
// validate/apply Wi-Fi, wait for an address, then atomically persist success.
// Never log credentials. Provisioning is cleartext at the BLE link layer and
// must be physically authorized; no automatic online/network-loss offering.
type Options struct {
	Name      string
	Version   string
	Hardware  string
	Authorize <-chan struct{}
	Join      func(context.Context, WiFi) (string, error)
	// Health runs after transport registration and periodically on the session
	// loop, not an independent goroutine. An error terminates provisioning.
	Health func() error
}

// Capabilities describes the optional RPCs backed by this configuration.
// The per-device name enables device information; Wi-Fi joining is required.
func (o Options) Capabilities() Capabilities {
	if o.Name != "" {
		return CanDeviceInfo
	}
	return 0
}

// Write is one chunk from a transport peer. Peer is an opaque transport ID;
// chunks from different peers never form one RPC.
type Write struct {
	Peer string
	Data []byte
}

// Run handles bounded Improv RPCs, including fragmented ATT writes. update is
// called serially with characteristic values. Transport must bound writes and
// stop accepting them when ctx ends. No credentials are sent back to clients.
// On successful provisioning it returns the Wi-Fi RPC's peer after the result dwell.
func Run(ctx context.Context, options Options, writes <-chan Write, update func(string, []byte)) (string, error) {
	if options.Join == nil || options.Authorize == nil {
		return "", errors.New("improv: Join and physical authorization are required")
	}
	// Waiting for a physically present user is not a provisioning failure.
	// Authorization and individual joins remain bounded below.
	health := time.NewTicker(time.Second)
	defer health.Stop()
	checkHealth := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if options.Health != nil {
			return options.Health()
		}
		return nil
	}
	if err := checkHealth(); err != nil {
		return "", err
	}
	state := AuthorizationRequired
	setState := func(s State) { state = s; update(StateUUID, []byte{byte(s)}) }
	setError := func(e Error) { update(ErrorUUID, []byte{byte(e)}) }
	setState(state)
	setError(ErrNone)
	authorization := time.NewTimer(time.Hour)
	authorization.Stop()
	defer authorization.Stop()
	var authorizedUntil time.Time
	var held []byte
	var peer string
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-health.C:
			if err := checkHealth(); err != nil {
				return "", err
			}
		case <-authorization.C:
			if state == Authorized {
				setState(AuthorizationRequired)
			}
		case _, ok := <-options.Authorize:
			if !ok {
				options.Authorize = nil
				continue
			}
			if state == AuthorizationRequired {
				setState(Authorized)
				authorizedUntil = time.Now().Add(time.Minute)
				authorization.Reset(time.Minute)
			}
		case write, ok := <-writes:
			if !ok {
				return "", nil
			}
			if write.Peer != peer {
				held = nil
				peer = write.Peer
			}
			if len(held)+len(write.Data) > 258 {
				held = nil
				setError(ErrInvalidRPC)
				continue
			}
			held = append(held, write.Data...)
			if len(held) < 2 || len(held) < int(held[1])+3 {
				continue
			}
			frame, err := Parse(held)
			held = nil
			if err != nil {
				setError(ErrInvalidRPC)
				continue
			}
			setError(ErrNone)
			switch frame.Command {
			case CmdDeviceInfo:
				if options.Capabilities()&CanDeviceInfo == 0 {
					setError(ErrUnknownCommand)
					continue
				}
				if len(frame.Data) != 0 {
					setError(ErrInvalidRPC)
					continue
				}
				update(RPCResultUUID, Result(CmdDeviceInfo, "Encore Satellite", options.Version, options.Hardware, options.Name))
			case CmdWiFiSettings:
				if state == Authorized && !time.Now().Before(authorizedUntil) {
					setState(AuthorizationRequired)
				}
				if state != Authorized {
					setError(ErrNotAuthorized)
					continue
				}
				wifi, err := ParseWiFi(frame.Data)
				if err != nil || len(wifi.SSID) > 32 || len(wifi.Passphrase) > 64 {
					setError(ErrInvalidRPC)
					continue
				}
				setState(Provisioning)
				joinCtx, done := context.WithTimeout(ctx, 80*time.Second)
				type joined struct {
					url string
					err error
				}
				result := make(chan joined, 1)
				go func() {
					url, err := options.Join(joinCtx, wifi)
					result <- joined{url, err}
				}()
				var url string
			joinWait:
				for {
					select {
					case r := <-result:
						url, err = r.url, r.err
						break joinWait
					case <-joinCtx.Done():
						// Join must observe cancellation. Wait for its cleanup before
						// accepting another candidate; do not heartbeat a stuck Join.
						<-result
						err = joinCtx.Err()
						break joinWait
					case <-health.C:
						if healthErr := checkHealth(); healthErr != nil {
							done()
							<-result
							return "", healthErr
						}
					}
				}
				if joinCtx.Err() != nil {
					err = joinCtx.Err()
				}
				done()
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				if err != nil {
					setError(ErrUnableToConnect)
					setState(Authorized)
					authorizedUntil = time.Now().Add(time.Minute)
					authorization.Reset(time.Minute)
					continue
				}
				setState(Provisioned)
				update(RPCResultUUID, Result(CmdWiFiSettings, url))
				// Give the central time to read the result, then close all GATT
				// and advertising. Proxy stays off unless explicitly enabled.
				timer := time.NewTimer(10 * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return "", ctx.Err()
				case <-timer.C:
					return peer, nil
				}
			default:
				setError(ErrUnknownCommand)
			}
		}
	}
}
