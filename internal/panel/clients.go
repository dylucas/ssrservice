package panel

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
)

func (s *Server) handleListClients(c *gin.Context) {
	inboundID, _ := strconv.ParseInt(c.Query("inbound_id"), 10, 64)
	items, err := s.svc.ListClients(inboundID)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	// The client list carries no credentials, so it is safe to compress; see
	// jsonWriter.writeJSON.
	s.json.writeJSON(c, http.StatusOK, gin.H{"items": items, "total": len(items)})
}

func (s *Server) handleGetClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	view, err := s.svc.GetClient(id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleUpdateClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input service.ClientInput
	if !bindJSON(c, &input) {
		return
	}
	client, err := s.svc.UpdateClient(id, input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	// Note: the PSK is deliberately absent from the detail.
	s.audit(c, auditEntry{
		Action: model.AuditClientUpdate, TargetType: "client", TargetID: client.ID,
		Detail: auditFields("name", client.Name, "enabled", client.Enabled,
			"quota_bytes", client.QuotaBytes, "expires_at", client.ExpiresAt,
			"ip_limit", client.IPLimit, "reset_policy", client.ResetPolicy,
			"reset_day", client.ResetDay),
	})
	c.JSON(http.StatusOK, client)
}

func (s *Server) handleDeleteClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := s.svc.DeleteClient(id); err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{Action: model.AuditClientDelete, TargetType: "client", TargetID: id})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleToggleClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req toggleRequest
	if !bindJSONMessage(c, &req, "enabled is required") {
		return
	}
	if err := s.svc.ToggleClient(id, req.Enabled); err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditClientToggle, TargetType: "client", TargetID: id,
		Detail: auditFields("enabled", req.Enabled),
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleResetClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := s.svc.ResetClient(id); err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{Action: model.AuditClientReset, TargetType: "client", TargetID: id})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleResetClientToken(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	token, err := s.svc.ResetClientToken(id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	// Note: the new token is deliberately absent from the detail.
	s.audit(c, auditEntry{Action: model.AuditClientToken, TargetType: "client", TargetID: id})
	c.JSON(http.StatusOK, gin.H{"sub_token": token, "subscription_url": "/sub/" + token})
}

func (s *Server) handleKickClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := s.svc.KickClient(id); err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{Action: model.AuditClientKick, TargetType: "client", TargetID: id})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleClientLink(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	link, subURL, err := s.svc.ClientLink(id, c.Query("host"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"link": link, "subscription_url": subURL})
}

func (s *Server) handleClientUsage(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	usage, err := s.svc.Usage(id, days)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": usage})
}
