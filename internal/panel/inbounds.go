package panel

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
)

func parseID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid_id", "invalid resource id")
		return 0, false
	}
	return id, true
}

func (s *Server) handleListInbounds(c *gin.Context) {
	items, err := s.svc.ListInbounds()
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items), "methods": model.Methods})
}

func (s *Server) handleCreateInbound(c *gin.Context) {
	var input service.InboundInput
	if !bindJSON(c, &input) {
		return
	}
	in, err := s.svc.CreateInbound(input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditInboundCreate, TargetType: "inbound", TargetID: in.ID,
		Detail: auditFields("remark", in.Remark, "listen", in.Listen, "port", in.Port,
			"method", in.Method, "domain", in.Domain, "domain_port", in.DomainPort,
			"udp", in.UDPEnabled, "enabled", in.Enabled),
	})
	c.JSON(http.StatusCreated, in)
}

func (s *Server) handleGetInbound(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	view, err := s.svc.GetInbound(id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleUpdateInbound(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input service.InboundInput
	if !bindJSON(c, &input) {
		return
	}
	in, err := s.svc.UpdateInbound(id, input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditInboundUpdate, TargetType: "inbound", TargetID: in.ID,
		Detail: auditFields("remark", in.Remark, "listen", in.Listen, "port", in.Port,
			"method", in.Method, "domain", in.Domain, "domain_port", in.DomainPort,
			"udp", in.UDPEnabled, "enabled", in.Enabled),
	})
	c.JSON(http.StatusOK, in)
}

func (s *Server) handleDeleteInbound(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := s.svc.DeleteInbound(id); err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{Action: model.AuditInboundDelete, TargetType: "inbound", TargetID: id})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleToggleInbound(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req toggleRequest
	if !bindJSONMessage(c, &req, "enabled is required") {
		return
	}
	if err := s.svc.ToggleInbound(id, req.Enabled); err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditInboundToggle, TargetType: "inbound", TargetID: id,
		Detail: auditFields("enabled", req.Enabled),
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleRestartInbound(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := s.svc.RestartInbound(id); err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{Action: model.AuditInboundRestart, TargetType: "inbound", TargetID: id})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleListInboundClients(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	items, err := s.svc.ListClients(id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	// Credential-free list: safe to compress (see jsonWriter.writeJSON).
	s.json.writeJSON(c, http.StatusOK, gin.H{"items": items, "total": len(items)})
}

func (s *Server) handleCreateClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input service.ClientInput
	if !bindJSON(c, &input) {
		return
	}
	client, err := s.svc.CreateClient(id, input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	// Note: the PSK is deliberately absent from the detail.
	s.audit(c, auditEntry{
		Action: model.AuditClientCreate, TargetType: "client", TargetID: client.ID,
		Detail: auditFields("inbound_id", id, "name", client.Name, "enabled", client.Enabled,
			"quota_bytes", client.QuotaBytes, "expires_at", client.ExpiresAt,
			"ip_limit", client.IPLimit, "reset_policy", client.ResetPolicy,
			"reset_day", client.ResetDay),
	})
	c.JSON(http.StatusCreated, client)
}

type toggleRequest struct {
	Enabled bool `json:"enabled"`
}
