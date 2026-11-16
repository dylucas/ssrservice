// Package subscription builds Shadowsocks URIs and subscription payloads.
package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Proxy describes one server endpoint plus its credentials.
type Proxy struct {
	Name      string
	Method    string
	ServerPSK string
	ClientPSK string
	Host      string
	Port      int
	UDP       bool
}

func (p Proxy) isSS2022() bool { return strings.HasPrefix(p.Method, "2022-") }

// credentials returns the "method:password" pair used inside ss:// links.
func (p Proxy) credentials() string {
	if p.isSS2022() && p.ServerPSK != "" {
		return fmt.Sprintf("%s:%s:%s", p.Method, p.ServerPSK, p.ClientPSK)
	}
	return fmt.Sprintf("%s:%s", p.Method, p.ClientPSK)
}

// Link renders a SIP002 ss:// URI. For Shadowsocks 2022 the userinfo carries
// base64url(method:serverPSK:clientPSK) as recommended by the 2022 spec.
func Link(p Proxy) string {
	host := p.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := p.Port
	if port <= 0 {
		port = 8388
	}
	userinfo := base64.RawURLEncoding.EncodeToString([]byte(p.credentials()))
	authority := net.JoinHostPort(host, strconv.Itoa(port))
	fragment := ""
	if p.Name != "" {
		fragment = "#" + urlEscape(p.Name)
	}
	query := ""
	if !p.UDP {
		query = "?udp-over-tcp=false"
	}
	return "ss://" + userinfo + "@" + authority + query + fragment
}

// ClashProxy renders a Clash YAML mapping (without leading "- ").
func ClashProxy(p Proxy) string {
	host := p.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := p.Port
	if port <= 0 {
		port = 8388
	}
	password := p.ClientPSK
	if p.isSS2022() && p.ServerPSK != "" {
		password = p.ServerPSK + ":" + p.ClientPSK
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  - name: %q\n", p.Name)
	fmt.Fprintf(&b, "    type: ss\n")
	fmt.Fprintf(&b, "    server: %s\n", host)
	fmt.Fprintf(&b, "    port: %d\n", port)
	fmt.Fprintf(&b, "    cipher: %s\n", p.Method)
	fmt.Fprintf(&b, "    password: %q\n", password)
	fmt.Fprintf(&b, "    udp: %t\n", p.UDP)
	return b.String()
}

// Base64Subscription renders the classic newline separated base64 payload.
func Base64Subscription(links []string) string {
	joined := strings.Join(links, "\n")
	return base64.StdEncoding.EncodeToString([]byte(joined))
}

// ClashSubscription renders a minimal Clash config with the given proxies.
func ClashSubscription(proxies []Proxy, nodeName string) string {
	var b strings.Builder
	b.WriteString("port: 7890\nsocks-port: 7891\nallow-lan: false\nmode: rule\nlog-level: info\n")
	b.WriteString("proxies:\n")
	for _, p := range proxies {
		b.WriteString(ClashProxy(p))
	}
	b.WriteString("proxy-groups:\n")
	fmt.Fprintf(&b, "  - name: %q\n    type: select\n    proxies:\n", "PROXY")
	for _, p := range proxies {
		fmt.Fprintf(&b, "      - %q\n", p.Name)
	}
	b.WriteString("rules:\n  - MATCH,PROXY\n")
	return b.String()
}

// urlEscape percent-encodes a fragment component. It is intentionally
// conservative: the output stays valid for common clients.
func urlEscape(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
			b.WriteByte(ch)
		case ch == '-' || ch == '_' || ch == '.' || ch == '~':
			b.WriteByte(ch)
		case ch == ' ':
			b.WriteString("%20")
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// ParseLink parses a SIP002 ss:// URI back into a Proxy.
//
// It accepts the forms this server emits (base64url userinfo, optionally with
// the SS2022 server PSK inside it) and the plaintext userinfo variant some
// clients produce. The method itself is not validated here: callers decide which
// methods they support.
func ParseLink(link string) (Proxy, error) {
	trimmed := strings.TrimSpace(link)
	if trimmed == "" {
		return Proxy{}, fmt.Errorf("empty link")
	}
	if !strings.HasPrefix(trimmed, "ss://") {
		return Proxy{}, fmt.Errorf("link must start with ss://")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return Proxy{}, fmt.Errorf("parse link: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return Proxy{}, fmt.Errorf("link is missing a host")
	}
	portText := u.Port()
	if portText == "" {
		return Proxy{}, fmt.Errorf("link is missing a port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return Proxy{}, fmt.Errorf("invalid port %q", portText)
	}

	var userinfo string
	if u.User != nil {
		// url.Parse splits "method:password" into username/password, and a
		// base64url blob without ':' lands entirely in the username.
		if password, ok := u.User.Password(); ok {
			userinfo = u.User.Username() + ":" + password
		} else {
			userinfo = u.User.Username()
		}
	}
	proxy, err := parseUserinfo(userinfo)
	if err != nil {
		return Proxy{}, err
	}
	proxy.Host = host
	proxy.Port = port
	if fragment := u.Fragment; fragment != "" {
		if name, err := url.PathUnescape(fragment); err == nil {
			proxy.Name = name
		} else {
			proxy.Name = fragment
		}
	}
	proxy.UDP = !strings.EqualFold(u.Query().Get("udp-over-tcp"), "true")
	return proxy, nil
}

// parseUserinfo decodes "method:password" (or the SS2022 three-part form), in
// either base64url or plaintext.
func parseUserinfo(userinfo string) (Proxy, error) {
	if userinfo == "" {
		return Proxy{}, fmt.Errorf("link is missing credentials")
	}
	decoded, ok := decodeUserinfo(userinfo)
	if !ok {
		// Plaintext userinfo, e.g. "aes-256-gcm:secret".
		decoded = userinfo
	}
	parts := strings.SplitN(decoded, ":", 3)
	switch len(parts) {
	case 2:
		method, password := parts[0], parts[1]
		if method == "" || password == "" {
			return Proxy{}, fmt.Errorf("link credentials are incomplete")
		}
		return Proxy{Method: method, ClientPSK: password}, nil
	case 3:
		// SS2022: method:serverPSK:clientPSK.
		method, serverPSK, clientPSK := parts[0], parts[1], parts[2]
		if method == "" || clientPSK == "" {
			return Proxy{}, fmt.Errorf("link credentials are incomplete")
		}
		return Proxy{Method: method, ServerPSK: serverPSK, ClientPSK: clientPSK}, nil
	default:
		return Proxy{}, fmt.Errorf("link credentials are malformed")
	}
}

// decodeUserinfo tries the base64 variants a client may use.
func decodeUserinfo(userinfo string) (string, bool) {
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if decoded, err := encoding.DecodeString(userinfo); err == nil {
			return string(decoded), true
		}
	}
	return "", false
}

// SingBoxSubscription renders a sing-box remote profile: a JSON object whose
// "outbounds" array holds one Shadowsocks outbound per proxy.
//
// sing-box reads remote profiles as a partial configuration, so the object only
// carries what the profile defines. The password field follows the same rule as
// the Clash mapping: for Shadowsocks 2022 the client password is
// "serverPSK:clientPSK", for the legacy AEAD ciphers it is just the client PSK.
func SingBoxSubscription(proxies []Proxy) string {
	outbounds := make([]singBoxOutbound, 0, len(proxies))
	seen := make(map[string]int, len(proxies))
	for _, p := range proxies {
		host := p.Host
		if host == "" {
			host = "127.0.0.1"
		}
		port := p.Port
		if port <= 0 {
			port = 8388
		}
		password := p.ClientPSK
		if p.isSS2022() && p.ServerPSK != "" {
			password = p.ServerPSK + ":" + p.ClientPSK
		}
		// A tag has to be unique: sing-box rejects a profile with two outbounds
		// of the same name, and a client may legitimately share a name across
		// inbounds.
		tag := p.Name
		if tag == "" {
			tag = "sspanel"
		}
		if count := seen[tag]; count > 0 {
			seen[tag] = count + 1
			tag = fmt.Sprintf("%s (%d)", tag, count+1)
		} else {
			seen[tag] = 1
		}
		outbounds = append(outbounds, singBoxOutbound{
			Type:       "shadowsocks",
			Tag:        tag,
			Server:     host,
			ServerPort: port,
			Method:     p.Method,
			Password:   password,
		})
	}
	payload, err := json.Marshal(singBoxProfile{Outbounds: outbounds})
	if err != nil {
		// Marshal only fails on unsupported types, and this struct is plain
		// strings and ints, so a failure here would be a programming error. An
		// empty profile keeps the endpoint honest instead of returning invalid
		// JSON.
		return `{"outbounds":[]}`
	}
	return string(payload)
}

// singBoxProfile is the subset of a sing-box configuration this server emits.
type singBoxProfile struct {
	Outbounds []singBoxOutbound `json:"outbounds"`
}

// singBoxOutbound is one Shadowsocks outbound in a sing-box profile.
type singBoxOutbound struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Method     string `json:"method"`
	Password   string `json:"password"`
}
