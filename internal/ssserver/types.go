// Package ssserver implements the Shadowsocks runtime: listeners, protocol
// services, per-client accounting, access gates and hot reload.
package ssserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/ydong/ssrservice/internal/model"
)

// Errors returned by the access gates.
var (
	ErrNoUser             = errors.New("no user identity in connection")
	ErrUnknownClient      = errors.New("unknown client")
	ErrClientDisabled     = errors.New("client disabled")
	ErrClientExpired      = errors.New("client expired")
	ErrQuotaExceeded      = errors.New("client quota exceeded")
	ErrIPLimit            = errors.New("client IP limit reached")
	ErrTooManySessions    = errors.New("client session limit reached")
	ErrDestinationBlocked = errors.New("destination blocked by policy")
	ErrInboundDisabled    = errors.New("inbound disabled")
)

// ClientSnapshot is the runtime view of a client.
type ClientSnapshot struct {
	ID         int64
	Name       string
	PSK        string
	Enabled    bool
	QuotaBytes int64
	ExpiresAt  int64
	IPLimit    int
}

// Addr returns the host:port of the snapshot. IPv6 literals are bracketed.
func (in InboundSnapshot) Addr() string {
	return net.JoinHostPort(in.Listen, strconv.Itoa(in.Port))
}

// protocolService is the subset of shadowsocks.Service the runtime needs.
type protocolService interface {
	NewConnection(ctx context.Context, conn net.Conn, metadata M.Metadata) error
	NewPacket(ctx context.Context, conn N.PacketConn, buffer *buf.Buffer, metadata M.Metadata) error
	NewError(ctx context.Context, err error)
}

// InboundSnapshot is the runtime view of an inbound.
type InboundSnapshot struct {
	ID         int64
	Remark     string
	Listen     string
	Port       int
	Method     string
	ServerPSK  string
	UDPEnabled bool
	Enabled    bool
	Clients    []ClientSnapshot
}

// PolicyConfig controls which destinations the proxy may dial.
type PolicyConfig struct {
	AllowPrivateTargets bool
	BlockMetadata       bool
	PanelListen         string
	PanelPort           int
}

// UsageSink receives flushed traffic counters.
type UsageSink interface {
	SaveUsage(snaps []model.UsageSnapshot) error
}

// Options configures a Manager.
type Options struct {
	Logger          *slog.Logger
	Sink            UsageSink
	Location        *time.Location
	FlushInterval   time.Duration
	EnforceInterval time.Duration
	UDPTimeout      time.Duration
	Policy          PolicyConfig
}

// snapshotOf converts persisted rows to an inbound snapshot.
func snapshotOf(in model.Inbound, clients []model.Client) InboundSnapshot {
	snap := InboundSnapshot{
		ID:         in.ID,
		Remark:     in.Remark,
		Listen:     in.Listen,
		Port:       in.Port,
		Method:     in.Method,
		ServerPSK:  in.ServerPSK,
		UDPEnabled: in.UDPEnabled,
		Enabled:    in.Enabled,
		Clients:    make([]ClientSnapshot, 0, len(clients)),
	}
	for _, c := range clients {
		snap.Clients = append(snap.Clients, clientSnapshotOf(c))
	}
	return snap
}

func clientSnapshotOf(c model.Client) ClientSnapshot {
	return ClientSnapshot{
		ID:         c.ID,
		Name:       c.Name,
		PSK:        c.PSK,
		Enabled:    c.Enabled,
		QuotaBytes: c.QuotaBytes,
		ExpiresAt:  c.ExpiresAt,
		IPLimit:    c.IPLimit,
	}
}
