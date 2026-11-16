package panel

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/service"
	"github.com/ydong/ssrservice/internal/subscription"
)

// subscriptionUpdateHours is the refresh interval advertised to clients. The
// panel does not change it: a client that re-fetches too eagerly would hit the
// token endpoint on every network change, and the numbers it carries only move
// on the traffic flush interval anyway.
const subscriptionUpdateHours = 12

func (s *Server) handleSubscription(c *gin.Context) {
	// The body embeds the client PSK, so it is a credential response like any
	// other: no cache may keep a copy. Both branches and the 404 carry it, so it
	// is set before the first possible write.
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	// Every "not available" case collapses into the same 404: an unknown
	// token, a disabled subscription and an inactive client must not be
	// distinguishable from outside. For the same reason the account headers are
	// only added after the profile was resolved.
	profile, err := s.svc.SubscriptionFor(c.Param("token"))
	if err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}

	// Clients show "used / total, expires" from these headers instead of asking
	// the user to open the panel. total=0 means unlimited and expire=0 means
	// never, which is the convention Clash, mihomo, Stash and sing-box share.
	c.Header("Subscription-Userinfo", userInfoHeader(profile))
	c.Header("Profile-Title", profileTitle(profile.Proxy.Name))
	c.Header("Profile-Update-Interval", strconv.Itoa(subscriptionUpdateHours))

	switch detectSubscriptionFormat(c.Request.UserAgent(), c.Query("format")) {
	case "clash":
		c.Header("Content-Type", "text/yaml; charset=utf-8")
		c.String(http.StatusOK, subscription.ClashSubscription([]subscription.Proxy{profile.Proxy}, profile.Proxy.Name))
	case "singbox":
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.String(http.StatusOK, subscription.SingBoxSubscription([]subscription.Proxy{profile.Proxy}))
	default:
		payload := subscription.Base64Subscription([]string{subscription.Link(profile.Proxy)})
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.String(http.StatusOK, payload)
	}
}

// userInfoHeader renders the Subscription-Userinfo value. Zero quota and zero
// expiry have the documented "unlimited" and "never" meanings, so they are
// emitted as 0 rather than omitted: a client that treats a missing field as
// unknown would otherwise never show the numbers it does have.
func userInfoHeader(profile service.SubscriptionProfile) string {
	var b strings.Builder
	b.WriteString("upload=")
	b.WriteString(strconv.FormatInt(profile.UpBytes, 10))
	b.WriteString("; download=")
	b.WriteString(strconv.FormatInt(profile.DownBytes, 10))
	b.WriteString("; total=")
	b.WriteString(strconv.FormatInt(profile.QuotaBytes, 10))
	b.WriteString("; expire=")
	b.WriteString(strconv.FormatInt(profile.ExpiresAt, 10))
	return b.String()
}

// profileTitle renders the Profile-Title value. The base64: form is what lets a
// non-ASCII name survive a header field; a client that does not understand the
// prefix still gets a stable, readable ASCII title.
func profileTitle(name string) string {
	if name == "" {
		return "sspanel"
	}
	return "base64:" + base64.StdEncoding.EncodeToString([]byte(name))
}

func detectSubscriptionFormat(userAgent, requested string) string {
	if requested != "" {
		// "sing-box" is the product name and "singbox" the query value the
		// docs use; accepting both keeps a hand-written URL from silently
		// falling back to the base64 payload.
		switch strings.ToLower(strings.TrimSpace(requested)) {
		case "singbox", "sing-box":
			return "singbox"
		}
		return strings.ToLower(strings.TrimSpace(requested))
	}
	ua := strings.ToLower(userAgent)
	switch {
	case strings.Contains(ua, "sing-box"):
		return "singbox"
	case strings.Contains(ua, "clash") || strings.Contains(ua, "meta") || strings.Contains(ua, "mihomo"):
		return "clash"
	}
	return "base64"
}
