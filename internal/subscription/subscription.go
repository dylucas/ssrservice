// Package subscription builds Shadowsocks URIs and subscription payloads.
package subscription

import (
	"encoding/base64"
	"fmt"
	"net"
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
	// Transport is reserved for SIP003 plugins; unused for now.
	Plugin string
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
