// Package panel implements the HTTP API and serves the embedded frontend.
package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/config"
	"github.com/ydong/ssrservice/internal/service"
	"github.com/ydong/ssrservice/internal/system"
	"github.com/ydong/ssrservice/internal/version"
	"github.com/ydong/ssrservice/internal/webui"
)

// maxAPIRequestBody caps the size of JSON request bodies handled by the API.
const maxAPIRequestBody = 1 << 20

// runtimeConfigID is the DOM id of the JSON configuration block injected into
// index.html. It is a data block rather than an executable script so that a
// strict Content-Security-Policy does not block it.
const runtimeConfigID = "sspanel-config"

// cachedAssetPrefix holds content-addressed files emitted by Vite.
const cachedAssetPrefix = "assets/"

// Server hosts the panel HTTP API.
type Server struct {
	cfg            *config.Config
	svc            *service.Service
	logger         *slog.Logger
	sessions       *auth.Sessions
	limiter        *auth.LoginLimiter
	monitor        *system.Collector
	httpSrv        *http.Server
	trustedProxies []*net.IPNet
	// assets caches gzip variants of the embedded frontend assets.
	assets *assetCache
	// json writes API responses and compresses the credential-free lists.
	json *jsonWriter
	// handler is built once and reused: main used to call Handler() and then
	// ListenAndServe call it again, discarding the first engine along with its
	// routes and http.Server.
	handlerOnce sync.Once
	handler     http.Handler
}

// runtimeConfig is serialized into index.html.
type runtimeConfig struct {
	BasePath string `json:"basePath"`
	Version  string `json:"version"`
}

// New creates the panel server.
func New(cfg *config.Config, svc *service.Service, logger *slog.Logger, sessions *auth.Sessions) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	proxies, err := parseTrustedProxies(cfg.Panel.TrustedProxies)
	if err != nil {
		// Validation happens in config.Validate; keep the panel usable and
		// fail closed by trusting no proxy at all.
		logger.Warn("ignoring invalid trusted proxies", "error", err)
		proxies = nil
	}
	return &Server{
		cfg:            cfg,
		svc:            svc,
		logger:         logger,
		sessions:       sessions,
		limiter:        auth.NewLoginLimiter(cfg.Security.LoginRateLimit, time.Minute),
		monitor:        system.NewCollector(),
		trustedProxies: proxies,
		assets:         newAssetCache(),
		json:           newJSONWriter(),
	}
}

// parseTrustedProxies turns IP/CIDR strings into networks.
func parseTrustedProxies(values []string) ([]*net.IPNet, error) {
	if len(values) == 0 {
		return nil, nil
	}
	networks := make([]*net.IPNet, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if strings.Contains(value, "/") {
			_, network, err := net.ParseCIDR(value)
			if err != nil {
				return nil, err
			}
			networks = append(networks, network)
			continue
		}
		ip := net.ParseIP(value)
		if ip == nil {
			return nil, &net.ParseError{Type: "IP address", Text: value}
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return networks, nil
}

// remoteIsTrustedProxy reports whether the request came from a configured
// reverse proxy.
func (s *Server) remoteIsTrustedProxy(r *http.Request) bool {
	if len(s.trustedProxies) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, network := range s.trustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// isSecureRequest reports whether the browser reached the panel over HTTPS,
// either directly or through a trusted TLS-terminating proxy.
func (s *Server) isSecureRequest(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	if !s.remoteIsTrustedProxy(c.Request) {
		return false
	}
	proto := c.GetHeader("X-Forwarded-Proto")
	if idx := strings.Index(proto, ","); idx >= 0 {
		proto = proto[:idx]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// Handler builds the HTTP handler tree once and returns the same handler on
// every call.
func (s *Server) Handler() http.Handler {
	s.handlerOnce.Do(func() { s.handler = s.buildHandler() })
	return s.handler
}

// buildHandler constructs the HTTP handler tree.
func (s *Server) buildHandler() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	// Gin trusts every proxy by default; only trust the explicitly configured
	// ones so a client cannot spoof X-Forwarded-For to evade rate limits or
	// pollute logs.
	var trusted []string
	if len(s.trustedProxies) > 0 {
		trusted = s.cfg.Panel.TrustedProxies
	}
	if err := engine.SetTrustedProxies(trusted); err != nil {
		s.logger.Warn("could not apply trusted proxies", "error", err)
		_ = engine.SetTrustedProxies(nil)
	}
	engine.Use(gin.Recovery())
	engine.Use(s.accessLog())
	engine.Use(s.securityHeaders())

	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "version": version.Full()})
	})

	base := s.cfg.Panel.BasePath
	if base != "/" {
		// Canonicalize the sub-path entry point so relative asset URLs resolve
		// against "/panel/" rather than "/".
		engine.GET(base, func(c *gin.Context) {
			target := base + "/"
			if c.Request.URL.RawQuery != "" {
				target += "?" + c.Request.URL.RawQuery
			}
			c.Redirect(http.StatusPermanentRedirect, target)
		})
	}
	router := engine.Group(base)
	apiV1 := router.Group("/api/v1")
	apiV1.Use(limitRequestBody(maxAPIRequestBody))
	apiV1.Use(noStore())
	apiV1.POST("/auth/login", s.handleLogin)
	api := apiV1.Group("")
	api.Use(s.requireAuth())

	api.POST("/auth/logout", s.handleLogout)
	api.GET("/auth/me", s.handleMe)
	api.PUT("/auth/password", s.handleChangePassword)
	api.GET("/dashboard", s.handleDashboard)
	api.GET("/system", s.handleSystem)
	api.GET("/inbounds", s.handleListInbounds)
	api.POST("/inbounds", s.handleCreateInbound)
	api.GET("/inbounds/:id", s.handleGetInbound)
	api.PUT("/inbounds/:id", s.handleUpdateInbound)
	api.DELETE("/inbounds/:id", s.handleDeleteInbound)
	api.POST("/inbounds/:id/toggle", s.handleToggleInbound)
	api.POST("/inbounds/:id/restart", s.handleRestartInbound)
	api.GET("/inbounds/:id/clients", s.handleListInboundClients)
	api.POST("/inbounds/:id/clients", s.handleCreateClient)
	api.GET("/clients", s.handleListClients)
	api.GET("/clients/:id", s.handleGetClient)
	api.PUT("/clients/:id", s.handleUpdateClient)
	api.DELETE("/clients/:id", s.handleDeleteClient)
	api.POST("/clients/:id/toggle", s.handleToggleClient)
	api.POST("/clients/:id/reset", s.handleResetClient)
	api.POST("/clients/:id/token", s.handleResetClientToken)
	api.POST("/clients/:id/kick", s.handleKickClient)
	api.GET("/clients/:id/link", s.handleClientLink)
	api.GET("/clients/:id/usage", s.handleClientUsage)
	api.GET("/settings", s.handleGetSettings)
	api.PUT("/settings", s.handleUpdateSettings)
	api.GET("/backup", s.handleBackup)
	api.GET("/audit", s.handleListAudit)
	// The export lives outside /clients so it cannot collide with the /clients/:id
	// wildcard (gin panics when a static segment and a wildcard share a position).
	api.GET("/export/clients", s.handleExportClients)
	api.POST("/inbounds/:id/clients/import", s.handleImportClients)

	// Public subscriptions.
	router.GET("/sub/:token", s.handleSubscription)

	s.mountFrontend(engine, router)

	s.httpSrv = &http.Server{
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return engine
}

// ListenAndServe starts the HTTP server and blocks until it stops.
func (s *Server) ListenAndServe() error {
	s.Handler()
	addr := net.JoinHostPort(s.cfg.Panel.Listen, strconv.Itoa(s.cfg.Panel.Port))
	s.httpSrv.Addr = addr
	if s.cfg.Panel.TLSCert != "" {
		return s.httpSrv.ListenAndServeTLS(s.cfg.Panel.TLSCert, s.cfg.Panel.TLSKey)
	}
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

// Addr returns the listen address of the panel.
func (s *Server) Addr() string {
	return net.JoinHostPort(s.cfg.Panel.Listen, strconv.Itoa(s.cfg.Panel.Port))
}

// basePathAssets returns the URL prefix of the hashed asset directory for the
// configured base path ("/assets/" by default, "/panel/assets/" when mounted
// under a sub path).
func basePathAssets(basePath string) string {
	base := strings.TrimSuffix(basePath, "/")
	return base + "/" + cachedAssetPrefix
}

// redactSubscriptionPath hides the token of a subscription request path.
//
// The token is a credential: whoever holds it can fetch the client's share
// link, which carries both the server and the client PSK. Access logs go to
// stderr, which systemd hands to journald and operators keep, so the path is
// recorded with the token removed.
//
// basePath is normalized without a trailing slash ("/" or "/panel"), so the
// prefix has to be joined with an explicit separator. Concatenating it directly
// produced "/panelsub/" and never matched a sub-path deployment, which left the
// token in the log.
func redactSubscriptionPath(basePath, path string) string {
	base := strings.TrimSuffix(basePath, "/")
	if strings.HasPrefix(path, base+"/sub/") {
		return base + "/sub/<redacted>"
	}
	return path
}

func (s *Server) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		path := redactSubscriptionPath(s.cfg.Panel.BasePath, c.Request.URL.Path)
		// Hashed assets are requested several times per page load; logging them
		// at info level would bury the API requests an operator cares about.
		log := s.logger.Info
		if strings.HasPrefix(path, basePathAssets(s.cfg.Panel.BasePath)) {
			log = s.logger.Debug
		}
		log("http request",
			"method", c.Request.Method,
			"path", path,
			"status", c.Writer.Status(),
			"ip", c.ClientIP(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}

func (s *Server) securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'")
		if s.isSecureRequest(c) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		c.Next()
	}
}

func (s *Server) mountFrontend(engine *gin.Engine, router *gin.RouterGroup) {
	assets := webui.FS()
	fileServer := http.FileServer(http.FS(assets))
	serve := func(c *gin.Context) {
		if !webui.Built() {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(notBuiltPage))
			return
		}
		base := strings.TrimSuffix(s.cfg.Panel.BasePath, "/")
		path := strings.TrimPrefix(c.Request.URL.Path, base)
		path = strings.TrimPrefix(path, "/")
		if path == "" {
			path = "index.html"
		}
		if path == "index.html" {
			s.serveIndex(c)
			return
		}
		if file, err := assets.Open(path); err == nil {
			_ = file.Close()
			if strings.HasPrefix(path, cachedAssetPrefix) {
				// Vite fingerprints these filenames, so they can be cached
				// forever.
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
				// The response representation depends on Accept-Encoding, so
				// shared caches must key on it even when this client gets the
				// plain file.
				c.Header("Vary", "Accept-Encoding")
			}
			if s.serveCompressedAsset(c, assets, path) {
				return
			}
			c.Request.URL.Path = "/" + path
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		// SPA fallback: unknown paths render the app shell.
		s.serveIndex(c)
	}
	router.GET("/", serve)
	// Unmatched paths fall back to the SPA, except API and subscription
	// routes, which must return a JSON/plain 404 instead of HTML.
	engine.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		base := strings.TrimSuffix(s.cfg.Panel.BasePath, "/")
		if strings.HasPrefix(path, base+"/api/") || strings.HasPrefix(path, base+"/sub/") {
			c.JSON(http.StatusNotFound, errorEnvelope{Error: apiError{Code: "not_found", Message: "endpoint not found"}})
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.JSON(http.StatusMethodNotAllowed, errorEnvelope{Error: apiError{Code: "method_not_allowed", Message: "method not allowed"}})
			return
		}
		serve(c)
	})
}

// serveIndex renders index.html with the runtime configuration injected.
func (s *Server) serveIndex(c *gin.Context) {
	data, err := webui.FS().Open("index.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "index.html missing")
		return
	}
	content, err := io.ReadAll(data)
	_ = data.Close()
	if err != nil {
		c.String(http.StatusInternalServerError, "index.html unreadable")
		return
	}
	basePath := s.cfg.Panel.BasePath
	if basePath == "" {
		basePath = "/"
	}
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	payload, err := json.Marshal(runtimeConfig{BasePath: basePath, Version: version.Full()})
	if err != nil {
		c.String(http.StatusInternalServerError, "runtime config")
		return
	}
	// json.Marshal escapes "<", ">" and "&", so the payload cannot terminate
	// the script element. The block is inert data: no CSP exception needed.
	config := fmt.Sprintf(`<script id=%q type="application/json">%s</script>`,
		runtimeConfigID, payload)
	// Vite is configured with a relative base, so the emitted asset URLs are
	// "./assets/...". A relative URL resolves against the browser's current
	// path, which breaks on any nested route: "/clients/" asks for
	// "/clients/assets/index-xxx.js", the SPA fallback answers with HTML, and
	// the module refuses to load. A <base href> pins every relative URL to the
	// panel root instead, which also makes the trailing-slash and sub-path
	// variants work. base-uri 'self' in the CSP already allows a same-origin
	// base.
	baseTag := fmt.Sprintf(`<base href=%q>`, basePath)
	html := strings.Replace(string(content), "<head>", "<head>"+baseTag+config, 1)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

const notBuiltPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>sspanel</title></head>
<body style="font-family:system-ui;max-width:640px;margin:80px auto;line-height:1.6">
<h1>前端尚未构建</h1>
<p>运行 <code>make web</code> 或 <code>cd web &amp;&amp; npm ci &amp;&amp; npm run build</code> 后重新编译二进制。</p>
<p>API 已可用：<code>/api/v1</code>，健康检查：<code>/healthz</code>。</p>
</body></html>`
