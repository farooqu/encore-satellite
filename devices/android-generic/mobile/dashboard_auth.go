package mobile

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"golang.org/x/net/websocket"
)

const dashboardEnrollmentTimeout = 5 * time.Minute

// The phone authenticates directly to HA over HTTPS. The temporary LAN HTTP
// listener receives only an authorization code bound to a verifier that never
// leaves this process except over TLS to HA; it never serves either token.
type DashboardEnrollment struct {
	origin, clientID, verifier string
	qr                         []byte
	ctx                        context.Context
	cancel                     context.CancelFunc
	codes                      chan string
	await                      sync.Once
}

func BeginDashboardEnrollment(origin, address string) (*DashboardEnrollment, error) {
	if !dashboardHTTPSOrigin(origin) {
		return nil, errors.New("phone sign-in requires an HTTPS Home Assistant URL")
	}
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return nil, errors.New("connect the tablet and phone to the same local Wi-Fi or Ethernet network")
	}
	// Older HA releases ignore PKCE parameters. Never send their unbound
	// authorization code through a cleartext callback.
	if !dashboardSupportsPKCE(origin) {
		return nil, errors.New("phone sign-in needs a reachable HTTPS Home Assistant server running 2026.11 or later")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		return nil, errors.New("could not open the tablet's temporary sign-in callback")
	}
	host := listener.Addr().String()
	clientID := "http://" + host + "/"
	verifier := dashboardNonce()
	challenge := sha256.Sum256([]byte(verifier))
	state := dashboardNonce()
	params := url.Values{
		"client_id": {clientID}, "redirect_uri": {clientID + "approved"},
		"response_type": {"code"}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	qr, err := qrcode.Encode(origin+"/auth/authorize?"+params.Encode(), qrcode.Medium, 512)
	if err != nil {
		listener.Close()
		return nil, errors.New("could not generate the sign-in QR code")
	}
	ctx, cancel := context.WithTimeout(context.Background(), dashboardEnrollmentTimeout)
	enrollment := &DashboardEnrollment{origin: origin, clientID: clientID, verifier: verifier,
		qr: qr, ctx: ctx, cancel: cancel, codes: make(chan string, 1)}
	server := &http.Server{
		ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 3 * time.Second, MaxHeaderBytes: 4 << 10,
		Handler: dashboardCallback(ctx, host, state, enrollment.codes),
	}
	go server.Serve(listener)
	go func() { <-ctx.Done(); server.Close() }()
	return enrollment, nil
}

func dashboardCallback(ctx context.Context, host, state string, codes chan<- string) http.Handler {
	var received sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if r.Method != http.MethodGet || r.Host != host {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/done" {
			io.WriteString(w, "Return to Encore on the tablet to confirm the Home Assistant account.")
			return
		}
		q := r.URL.Query()
		code := q.Get("code")
		if r.URL.Path != "/approved" || len(q["state"]) != 1 || len(q["code"]) != 1 ||
			subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 ||
			len(code) == 0 || len(code) > 256 || ctx.Err() != nil {
			http.Error(w, "This sign-in link is invalid or expired. Start again on the tablet.", http.StatusBadRequest)
			return
		}
		accepted := false
		received.Do(func() { accepted = true })
		if !accepted {
			http.Error(w, "This sign-in link has already been used.", http.StatusGone)
			return
		}
		http.Redirect(w, r, "/done", http.StatusSeeOther)
		codes <- code
	})
}

func (e *DashboardEnrollment) QRCode() []byte { return e.qr }
func (e *DashboardEnrollment) Close()         { e.cancel() }

func (e *DashboardEnrollment) Await() (*DashboardSession, error) {
	first := false
	e.await.Do(func() { first = true })
	if !first {
		return nil, errors.New("start a new phone sign-in")
	}
	select {
	case <-e.ctx.Done():
		return nil, errors.New("the QR code expired or sign-in was canceled")
	case code := <-e.codes:
		return exchangeDashboardCode(e.ctx, e.origin, e.clientID, e.verifier, code)
	}
}

func dashboardNonce() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func dashboardHTTPSOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && origin == "https://"+u.Host
}

func dashboardSupportsPKCE(origin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dashboardWebsocket(ctx, origin)
	if err != nil {
		return false
	}
	defer conn.Close()
	var hello struct {
		Type    string
		Version string `json:"ha_version"`
	}
	return websocket.JSON.Receive(conn, &hello) == nil && hello.Type == "auth_required" && dashboardPKCEVersion(hello.Version)
}

func dashboardPKCEVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return false
	}
	year, yerr := strconv.Atoi(parts[0])
	month, merr := strconv.Atoi(parts[1])
	return yerr == nil && merr == nil && month >= 1 && month <= 12 && (year > 2026 || year == 2026 && month >= 11)
}

type dashboardCredentials struct {
	Origin, ClientID, RefreshToken, Name string
}

// DashboardSession holds one HA user's separately revocable tablet session.
// The host persists Serialize only in Keystore-encrypted storage.
type DashboardSession struct {
	mu          sync.Mutex
	credentials dashboardCredentials
	token       dashboardToken
	expires     time.Time
	expired     bool
}

type dashboardToken struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Error        string `json:"error,omitempty"`
}

func exchangeDashboardCode(ctx context.Context, origin, clientID, verifier, code string) (*DashboardSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	token, err := requestDashboardToken(ctx, origin, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code_verifier": {verifier}, "code": {code},
	})
	if err != nil || token.RefreshToken == "" {
		return nil, errors.New("Home Assistant did not authorize this tablet. Start a new phone sign-in; HA 2026.11 or later is required.")
	}
	session := &DashboardSession{credentials: dashboardCredentials{Origin: origin, ClientID: clientID, RefreshToken: token.RefreshToken}}
	name, err := dashboardAccount(ctx, origin, token.AccessToken)
	if err != nil || ctx.Err() != nil {
		session.Revoke()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("the QR code expired or sign-in was canceled")
	}
	session.credentials.Name = name
	session.cache(token)
	return session, nil
}

func RestoreDashboardSession(serialized string) (*DashboardSession, error) {
	var credentials dashboardCredentials
	if json.Unmarshal([]byte(serialized), &credentials) != nil || !dashboardHTTPSOrigin(credentials.Origin) ||
		credentials.RefreshToken == "" || credentials.ClientID == "" || credentials.Name == "" {
		return nil, errors.New("saved dashboard session could not be restored")
	}
	return &DashboardSession{credentials: credentials}, nil
}

func (s *DashboardSession) Serialize() string {
	data, _ := json.Marshal(s.credentials)
	return string(data)
}

func (s *DashboardSession) Origin() string   { return s.credentials.Origin }
func (s *DashboardSession) Name() string     { return s.credentials.Name }
func (s *DashboardSession) ClientID() string { return s.credentials.ClientID }

var invalidDashboardGrant = errors.New("the Home Assistant session is no longer authorized")

func (s *DashboardSession) Expired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expired
}

func (s *DashboardSession) Access(force bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expired {
		return "", invalidDashboardGrant
	}
	if force || time.Until(s.expires) <= 30*time.Second {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		token, err := requestDashboardToken(ctx, s.Origin(), url.Values{
			"grant_type": {"refresh_token"}, "client_id": {s.credentials.ClientID}, "refresh_token": {s.credentials.RefreshToken},
		})
		if err != nil {
			s.expired = errors.Is(err, invalidDashboardGrant)
			return "", err
		}
		s.cache(token)
	}
	token := s.token
	token.ExpiresIn = max(1, int(time.Until(s.expires)/time.Second))
	data, _ := json.Marshal(token)
	return string(data), nil
}

func (s *DashboardSession) cache(token dashboardToken) {
	token.RefreshToken = ""
	s.token = token
	s.expires = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
}

func (s *DashboardSession) Revoke() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dashboardPost(ctx, s.Origin()+"/auth/revoke", url.Values{"token": {s.credentials.RefreshToken}})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New("Home Assistant did not revoke the dashboard session")
	}
	s.token = dashboardToken{}
	s.expired = true
	return nil
}

func dashboardPost(ctx context.Context, endpoint string, values url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, errors.New("invalid Home Assistant address")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("could not reach Home Assistant over HTTPS")
	}
	return res, nil
}

func requestDashboardToken(ctx context.Context, origin string, values url.Values) (dashboardToken, error) {
	res, err := dashboardPost(ctx, origin+"/auth/token", values)
	var token dashboardToken
	if err != nil {
		return token, err
	}
	defer res.Body.Close()
	if json.NewDecoder(io.LimitReader(res.Body, 32<<10)).Decode(&token) != nil {
		return dashboardToken{}, errors.New("invalid Home Assistant token response")
	}
	if res.StatusCode == http.StatusBadRequest && token.Error == "invalid_grant" ||
		res.StatusCode == http.StatusForbidden && token.Error == "access_denied" {
		return dashboardToken{}, invalidDashboardGrant
	}
	if res.StatusCode != http.StatusOK || token.AccessToken == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 86400 {
		return dashboardToken{}, errors.New("Home Assistant could not issue a dashboard access token")
	}
	return token, nil
}

func dashboardWebsocket(ctx context.Context, origin string) (*websocket.Conn, error) {
	cfg, err := websocket.NewConfig("wss://"+strings.TrimPrefix(origin, "https://")+"/api/websocket", origin)
	if err != nil {
		return nil, err
	}
	conn, err := cfg.DialContext(ctx)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	conn.MaxPayloadBytes = 32 << 10
	return conn, nil
}

func dashboardAccount(ctx context.Context, origin, accessToken string) (string, error) {
	problem := errors.New("could not verify the Home Assistant account")
	conn, err := dashboardWebsocket(ctx, origin)
	if err != nil {
		return "", problem
	}
	defer conn.Close()
	var hello struct{ Type string }
	if websocket.JSON.Receive(conn, &hello) != nil || hello.Type != "auth_required" ||
		websocket.JSON.Send(conn, map[string]string{"type": "auth", "access_token": accessToken}) != nil ||
		websocket.JSON.Receive(conn, &hello) != nil || hello.Type != "auth_ok" ||
		websocket.JSON.Send(conn, map[string]any{"id": 1, "type": "auth/current_user"}) != nil {
		return "", problem
	}
	var reply struct {
		ID      int
		Success bool
		Result  struct {
			Name  string
			Admin *bool `json:"is_admin"`
		}
	}
	if websocket.JSON.Receive(conn, &reply) != nil || reply.ID != 1 || !reply.Success || reply.Result.Name == "" || reply.Result.Admin == nil {
		return "", problem
	}
	if *reply.Result.Admin {
		return "", errors.New("use a non-admin Home Assistant account for satellites and scan a new QR code")
	}
	return reply.Result.Name, nil
}
