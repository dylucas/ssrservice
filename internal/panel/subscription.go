package panel

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/subscription"
)

func (s *Server) handleSubscription(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.String(http.StatusNotFound, "not found")
		return
	}
	if !s.svc.SubscriptionEnabled() {
		c.String(http.StatusNotFound, "not found")
		return
	}
	client, err := s.svc.Store().ClientBySubToken(token)
	if err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	inbound, err := s.svc.Store().InboundByID(client.InboundID)
	if err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	if !inbound.Enabled || client.StatusAt(nowFunc().Unix()) != model.StatusActive {
		c.String(http.StatusNotFound, "not found")
		return
	}
	host, port := s.svc.PublicEndpoint(inbound)
	proxy := subscription.Proxy{
		Name:      client.Name,
		Method:    inbound.Method,
		ServerPSK: inbound.ServerPSK,
		ClientPSK: client.PSK,
		Host:      host,
		Port:      port,
		UDP:       inbound.UDPEnabled,
	}
	switch detectSubscriptionFormat(c.Request.UserAgent(), c.Query("format")) {
	case "clash":
		c.Header("Content-Type", "text/yaml; charset=utf-8")
		c.Header("Profile-Update-Interval", "12")
		c.String(http.StatusOK, subscription.ClashSubscription([]subscription.Proxy{proxy}, client.Name))
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
