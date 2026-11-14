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
	System          systemInfo         `json:"system"`
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
		writeServiceError(c, err)
		return
	}
	up, down, err := s.svc.TrafficTotalsLive()
	if err != nil {
		writeServiceError(c, err)
		return
	}
	online := 0
	for _, ips := range s.svc.Manager().OnlineIPs() {
		online += len(ips)
	}
	since := time.Now().AddDate(0, 0, -30).In(s.svc.Config().Location()).Format("2006-01-02")
	days, err := s.svc.Store().DailyTotals(since)
	if err != nil {
		writeServiceError(c, err)
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
		Sessions:        s.svc.Manager().SessionCount(),
		UpBytes:         up,
		DownBytes:       down,
		Traffic30d:      days,
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
		"sessions": s.svc.Manager().SessionCount(),
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
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditSettingsUpdate, TargetType: "settings",
		Detail: auditFields("public_host", input.PublicHost, "public_port", input.PublicPort,
			"subscription_enabled", input.SubscriptionOn,
			"allow_private_targets", input.AllowPrivate,
			"reset_day", input.ResetDay, "timezone", input.Timezone),
	})
	c.JSON(http.StatusOK, s.svc.Settings())
}

func (s *Server) handleBackup(c *gin.Context) {
	dir, err := os.MkdirTemp("", "sspanel-backup-")
	if err != nil {
		writeServiceError(c, err)
		return
	}
	defer os.RemoveAll(dir)
	name := fmt.Sprintf("sspanel-%s.db", time.Now().Format("20060102-150405"))
	path := dir + "/" + name
	if err := s.svc.Backup(path); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Header("Content-Disposition", "attachment; filename="+name)
	c.File(path)
}
