package ssserver

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/model"
)

// ValidateServerPSK checks a server PSK for the given method.
func ValidateServerPSK(method, psk string) error {
	if !model.IsMethod(method) {
		return fmt.Errorf("unsupported method %q", method)
	}
	if !model.RequiresServerPSK(method) {
		if psk != "" {
			return fmt.Errorf("method %s does not use a server PSK", method)
		}
		return nil
	}
	// An empty server PSK means "generate one"; the caller fills it in.
	if psk == "" {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(psk)
	if err != nil {
		return fmt.Errorf("server PSK must be base64: %w", err)
	}
	if len(key) != model.KeySize(method) {
		return fmt.Errorf("server PSK must decode to %d bytes, got %d", model.KeySize(method), len(key))
	}
	return nil
}

// ValidateClientPSK checks a client PSK for the given method.
func ValidateClientPSK(method, psk string) error {
	if !model.IsMethod(method) {
		return fmt.Errorf("unsupported method %q", method)
	}
	if model.IsSS2022(method) {
		key, err := base64.StdEncoding.DecodeString(psk)
		if err != nil {
			return fmt.Errorf("client PSK must be base64: %w", err)
		}
		if len(key) != model.KeySize(method) {
			return fmt.Errorf("client PSK must decode to %d bytes, got %d", model.KeySize(method), len(key))
		}
		return nil
	}
	if psk == "" {
		return fmt.Errorf("password must not be empty")
	}
	return nil
}

// GenerateClientPSK returns a fresh PSK for the method.
func GenerateClientPSK(method string) (string, error) {
	if model.IsSS2022(method) {
		return auth.RandomBase64Key(model.KeySize(method))
	}
	return auth.RandomPassword(16)
}

// GenerateServerPSK returns a fresh server PSK for the method.
func GenerateServerPSK(method string) (string, error) {
	if !model.RequiresServerPSK(method) {
		return "", fmt.Errorf("method %s does not use a server PSK", method)
	}
	return auth.RandomBase64Key(model.KeySize(method))
}

// buildProtocol constructs a Shadowsocks service for the inbound. The returned
// defaultClientID is used for single-user methods that cannot carry a user
// identity in the protocol.
func buildProtocol(in InboundSnapshot, handler shadowsocks.Handler, udpTimeout time.Duration) (protocolService, int64, error) {
	method := in.Method
	if !model.IsMethod(method) {
		return nil, 0, fmt.Errorf("unsupported method %q", method)
	}
	timeout := int64(udpTimeout / time.Second)
	if timeout <= 0 {
		timeout = 60
	}

	if model.IsSS2022(method) {
		if model.IsMultiUser(method) {
			serverKey, err := base64.StdEncoding.DecodeString(in.ServerPSK)
			if err != nil {
				return nil, 0, fmt.Errorf("decode server PSK: %w", err)
			}
			service, err := shadowaead_2022.NewMultiService[uint64](method, serverKey, timeout, handler, time.Now)
			if err != nil {
				return nil, 0, fmt.Errorf("create SS2022 service: %w", err)
			}
			users := make([]uint64, 0, len(in.Clients))
			keys := make([][]byte, 0, len(in.Clients))
			for _, c := range in.Clients {
				key, err := base64.StdEncoding.DecodeString(c.PSK)
				if err != nil {
					return nil, 0, fmt.Errorf("client %q: invalid PSK: %w", c.Name, err)
				}
				users = append(users, uint64(c.ID))
				keys = append(keys, key)
			}
			if err := service.UpdateUsers(users, keys); err != nil {
				return nil, 0, fmt.Errorf("load users: %w", err)
			}
			return service, 0, nil
		}
		// Single user (2022-blake3-chacha20-poly1305).
		var password string
		var defaultID int64
		if len(in.Clients) > 0 {
			password = in.Clients[0].PSK
			defaultID = in.Clients[0].ID
		} else {
			// No users configured: create a service with an unusable random
			// key so the listener still starts and rejects everything.
			random, err := auth.RandomBase64Key(model.KeySize(method))
			if err != nil {
				return nil, 0, err
			}
			password = random
		}
		service, err := shadowaead_2022.NewServiceWithPassword(method, password, timeout, handler, time.Now)
		if err != nil {
			return nil, 0, fmt.Errorf("create SS2022 service: %w", err)
		}
		return service, defaultID, nil
	}

	// Legacy AEAD methods. The multi-user service tries each password during
	// the handshake, so several users can share a port.
	service, err := shadowaead.NewMultiService[uint64](method, timeout, handler)
	if err != nil {
		return nil, 0, fmt.Errorf("create %s service: %w", method, err)
	}
	users := make([]uint64, 0, len(in.Clients))
	passwords := make([]string, 0, len(in.Clients))
	for _, c := range in.Clients {
		users = append(users, uint64(c.ID))
		passwords = append(passwords, c.PSK)
	}
	if err := service.UpdateUsersWithPasswords(users, passwords); err != nil {
		return nil, 0, fmt.Errorf("load users: %w", err)
	}
	return service, 0, nil
}
