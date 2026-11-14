package subscription

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestLinkSS2022(t *testing.T) {
	link := Link(Proxy{
		Name:      "alice",
		Method:    "2022-blake3-aes-128-gcm",
		ServerPSK: "c2VydmVyLVBTSy0xNmJ5dGU=",
		ClientPSK: "Y2xpZW50LVBTSy0xNmJ5dGU=",
		Host:      "example.com",
		Port:      8388,
		UDP:       true,
	})
	if !strings.HasPrefix(link, "ss://") {
		t.Fatalf("unexpected link %q", link)
	}
	if !strings.HasSuffix(link, "#alice") {
		t.Fatalf("expected name fragment, got %q", link)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	userinfo := u.User.Username()
	decoded, err := base64.RawURLEncoding.DecodeString(userinfo)
	if err != nil {
		t.Fatalf("decode userinfo: %v", err)
	}
	want := "2022-blake3-aes-128-gcm:c2VydmVyLVBTSy0xNmJ5dGU=:Y2xpZW50LVBTSy0xNmJ5dGU="
	if string(decoded) != want {
		t.Fatalf("userinfo %q want %q", decoded, want)
	}
	if u.Host != "example.com:8388" {
		t.Fatalf("unexpected host %q", u.Host)
	}
}

func TestLinkLegacy(t *testing.T) {
	link := Link(Proxy{
		Name:      "bob",
		Method:    "aes-256-gcm",
		ClientPSK: "password123",
		Host:      "1.2.3.4",
		Port:      9000,
		UDP:       true,
	})
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(u.User.Username())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded) != "aes-256-gcm:password123" {
		t.Fatalf("unexpected userinfo %q", decoded)
	}
}

func TestClashSubscription(t *testing.T) {
	payload := ClashSubscription([]Proxy{{
		Name:      "alice",
		Method:    "2022-blake3-aes-128-gcm",
		ServerPSK: "c2VydmVy",
		ClientPSK: "Y2xpZW50",
		Host:      "example.com",
		Port:      8388,
		UDP:       true,
	}}, "alice")
	for _, want := range []string{"proxies:", "type: ss", "cipher: 2022-blake3-aes-128-gcm",
		`password: "c2VydmVy:Y2xpZW50"`, "udp: true", "MATCH,PROXY"} {
		if !strings.Contains(payload, want) {
			t.Fatalf("clash payload missing %q:\n%s", want, payload)
		}
	}
}

func TestBase64Subscription(t *testing.T) {
	payload := Base64Subscription([]string{"ss://a", "ss://b"})
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded) != "ss://a\nss://b" {
		t.Fatalf("unexpected payload %q", decoded)
	}
}

func TestFragmentEscaping(t *testing.T) {
	link := Link(Proxy{Name: "中文 名字", Method: "aes-256-gcm", ClientPSK: "x", Host: "h", Port: 1, UDP: true})
	if strings.Contains(link, " ") {
		t.Fatalf("space must be escaped: %q", link)
	}
	if !strings.HasSuffix(link, "#%E4%B8%AD%E6%96%87%20%E5%90%8D%E5%AD%97") {
		t.Fatalf("unexpected fragment: %q", link)
	}
}
