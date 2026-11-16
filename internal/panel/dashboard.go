package panel

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
	"github.com/ydong/ssrservice/internal/version"
)

type dashboardResponse struct {
	Version         string             `json:"version"`
	UptimeSeconds   int64              `json:"uptime_seconds"`
	Inbounds        int64              `json:"inbounds"`
	Clients         int64              `json:"clients"`
	EnabledInbounds int64              `json:"enabled_inbounds"`
	EnabledClients  int64              `json:"enabled_clients"`
	OnlineIPs       int                `json:"online_ips"`
	Sessions        int                `json:"sessions"`
	UpBytes         int64              `json:"up_bytes"`
	DownBytes       int64              `json:"down_bytes"`
	Traffic30d      []model.DailyUsage `json:"traffic_30d"`
	// Attention is the "needs attention" summary the dashboard shows above the
	// traffic chart. Each number links to the list filtered to the same rows.
	Attention attentionInfo `json:"attention"`
	System    systemInfo    `json:"system"`
}

// attentionInfo mirrors store.AttentionCounts so the response names stay
// documented JSON regardless of how the store spells its fields.
type attentionInfo struct {
	ExpiringSoon  int64 `json:"expiring_soon"`
	Expired       int64 `json:"expired"`
	QuotaExceeded int64 `json:"quota_exceeded"`
	Idle          int64 `json:"idle"`
	WindowDays    int   `json:"expiring_window_days"`
}

type systemInfo struct {
	OS          string  `json:"os"`
	Arch        string  `json:"arch"`
	NumCPU      int     `json:"num_cpu"`
	Goroutines  int     `json:"goroutines"`
	CPUPercent  float64 `json:"cpu_percent"`
	MemUsed     uint64  `json:"mem_used"`
	MemTotal    uint64  `json:"mem_total"`
	HeapAlloc   uint64  `json:"heap_alloc"`
	MetricsHost bool    `json:"host_metrics"`
}

func (s *Server) handleDashboard(c *gin.Context) {
	counts, err := s.svc.Counts()
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	up, down, err := s.svc.TrafficTotalsLive()
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	online := s.svc.OnlineIPCount()
	// The effective time zone (not the config file's) keeps this window aligned
	// with the per-client usage endpoint after a settings change.
	days, err := s.svc.DailyTotalsSince(30)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	attention, err := s.svc.Attention()
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	info := s.monitor.Snapshot()
	resp := dashboardResponse{
		Version:         version.Full(),
		UptimeSeconds:   info.Uptime,
		Inbounds:        counts.Inbounds,
		Clients:         counts.Clients,
		EnabledInbounds: counts.EnabledInbounds,
		EnabledClients:  counts.EnabledClients,
		OnlineIPs:       online,
		Sessions:        s.svc.SessionCount(),
		UpBytes:         up,
		DownBytes:       down,
		Traffic30d:      days,
		Attention: attentionInfo{
			ExpiringSoon:  attention.ExpiringSoon,
			Expired:       attention.Expired,
			QuotaExceeded: attention.QuotaExceeded,
			Idle:          attention.Idle,
			WindowDays:    attention.WindowDays,
		},
		System: systemInfo{
			OS:          info.OS,
			Arch:        info.Arch,
			NumCPU:      info.NumCPU,
			Goroutines:  info.Goroutines,
			CPUPercent:  info.CPUPercent,
			MemUsed:     info.MemUsed,
			MemTotal:    info.MemTotal,
			HeapAlloc:   info.HeapAlloc,
			MetricsHost: info.MetricsHost,
		},
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) handleSystem(c *gin.Context) {
	info := s.monitor.Snapshot()
	c.JSON(http.StatusOK, gin.H{
		"version":        version.Full(),
		"uptime_seconds": info.Uptime,
		"system": systemInfo{
			OS:          info.OS,
			Arch:        info.Arch,
			NumCPU:      info.NumCPU,
			Goroutines:  info.Goroutines,
			CPUPercent:  info.CPUPercent,
			MemUsed:     info.MemUsed,
			MemTotal:    info.MemTotal,
			HeapAlloc:   info.HeapAlloc,
			MetricsHost: info.MetricsHost,
		},
		"sessions": s.svc.SessionCount(),
	})
}

func (s *Server) handleGetSettings(c *gin.Context) {
	c.JSON(http.StatusOK, s.svc.Settings())
}

func (s *Server) handleUpdateSettings(c *gin.Context) {
	var input service.SettingsView
	if !bindJSON(c, &input) {
		return
	}
	if err := s.svc.UpdateSettings(input); err != nil {
		s.writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditSettingsUpdate, TargetType: "settings",
		Detail: auditFields("public_host", input.PublicHost, "public_port", input.PublicPort,
			"subscription_enabled", input.SubscriptionOn,
			"allow_private_targets", input.AllowPrivate,
			"reset_day", input.ResetDay, "timezone", input.Timezone,
			"access_log_enabled", input.AccessLog),
	})
	c.JSON(http.StatusOK, s.svc.Settings())
}

func (s *Server) handleBackup(c *gin.Context) {
	dir, err := os.MkdirTemp("", "sspanel-backup-")
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	defer os.RemoveAll(dir)
	name := fmt.Sprintf("sspanel-%s.db", time.Now().Format("20060102-150405"))
	path := dir + "/" + name
	if err := s.svc.Backup(path); err != nil {
		s.writeServiceError(c, err)
		return
	}
	c.Header("Content-Disposition", "attachment; filename="+name)
	c.File(path)
}
