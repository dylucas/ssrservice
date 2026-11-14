package panel

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/subscription"
)

func (s *Server) handleSubscription(c *gin.Context) {
	// Every "not available" case collapses into the same 404: an unknown
	// token, a disabled subscription and an inactive client must not be
	// distinguishable from outside.
	proxy, err := s.svc.SubscriptionFor(c.Param("token"))
	if err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	switch detectSubscriptionFormat(c.Request.UserAgent(), c.Query("format")) {
	case "clash":
		c.Header("Content-Type", "text/yaml; charset=utf-8")
		c.Header("Profile-Update-Interval", "12")
		c.String(http.StatusOK, subscription.ClashSubscription([]subscription.Proxy{proxy}, proxy.Name))
	default:
		payload := subscription.Base64Subscription([]string{subscription.Link(proxy)})
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.Header("Profile-Update-Interval", "12")
		c.String(http.StatusOK, payload)
	}
}

func detectSubscriptionFormat(userAgent, requested string) string {
	if requested != "" {
		return strings.ToLower(requested)
	}
	ua := strings.ToLower(userAgent)
	if strings.Contains(ua, "clash") || strings.Contains(ua, "meta") || strings.Contains(ua, "mihomo") {
		return "clash"
	}
	return "base64"
}
