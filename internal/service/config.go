package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
)

// Configuration snapshot export and import.
//
// A snapshot carries the panel's identity (settings, admin credentials,
// listeners and users) but no historical data. It is meant for moving a setup
// between machines or versions, where copying a SQLite file is awkward. The
// database backup endpoint remains the tool for a full migration including
// traffic and audit history.

// ExportConfig assembles the current configuration into a portable snapshot.
func (s *Service) ExportConfig() (model.ConfigSnapshot, error) {
	snapshot := model.ConfigSnapshot{
		Version:    model.ConfigSnapshotVersion,
		ExportedAt: time.Now().Unix(),
	}

	// Settings, read from the store rather than the in-memory cache so the file
	// matches the persisted state. The session secret is not copied over.
	values, err := s.store.AllSettings()
	if err != nil {
		return model.ConfigSnapshot{}, err
	}
	snapshot.Settings = model.ConfigSettings{
		PublicHost:          values[model.SettingPublicHost],
		PublicPort:          parseInt(values[model.SettingPublicPort]),
		SubscriptionEnabled: values[model.SettingSubscriptionEnabled] != "false",
		AllowPrivateTargets: values[model.SettingAllowPrivate] == "true",
		ResetDay:            parseInt(values[model.SettingResetDay]),
		Timezone:            values[model.SettingTimezone],
		AccessLogEnabled:    values[model.SettingAccessLog] == "true",
	}

	// The panel has one administrator; ID 1 is the seeded account.
	admin, err := s.store.AdminByID(1)
	if err != nil {
		return model.ConfigSnapshot{}, err
	}
	hashes, err := s.store.RecoveryCodeHashes(admin.ID)
	if err != nil {
		return model.ConfigSnapshot{}, err
	}
	snapshot.Admin = model.ConfigAdmin{
		Username:           admin.Username,
		PasswordHash:       admin.PasswordHash,
		MustChangePassword: admin.MustChangePassword,
		TOTPEnabled:        admin.TOTPEnabled,
		TOTPSecret:         admin.TOTPSecret,
		TOTPLastStep:       admin.TOTPLastStep,
		RecoveryCodeHashes: hashes,
	}

	inbounds, err := s.store.ListInbounds()
	if err != nil {
		return model.ConfigSnapshot{}, err
	}
	for _, in := range inbounds {
		item := model.ConfigInbound{
			Remark:     in.Remark,
			Listen:     in.Listen,
			Port:       in.Port,
			Method:     in.Method,
			ServerPSK:  in.ServerPSK,
			Domain:     in.Domain,
			DomainPort: in.DomainPort,
			UDPEnabled: in.UDPEnabled,
			Enabled:    in.Enabled,
			Clients:    []model.ConfigClient{},
		}
		clients, err := s.store.ListClients(in.ID)
		if err != nil {
			return model.ConfigSnapshot{}, err
		}
		for _, c := range clients {
			item.Clients = append(item.Clients, model.ConfigClient{
				Name:        c.Name,
				PSK:         c.PSK,
				Enabled:     c.Enabled,
				QuotaBytes:  c.QuotaBytes,
				UpBytes:     c.UpBytes,
				DownBytes:   c.DownBytes,
				ExpiresAt:   c.ExpiresAt,
				IPLimit:     c.IPLimit,
				ResetPolicy: c.ResetPolicy,
				ResetDay:    c.ResetDay,
				LastResetAt: c.LastResetAt,
				SubToken:    c.SubToken,
				Note:        c.Note,
			})
		}
		snapshot.Inbounds = append(snapshot.Inbounds, item)
	}
	if snapshot.Inbounds == nil {
		snapshot.Inbounds = []model.ConfigInbound{}
	}
	return snapshot, nil
}

// ImportConfig validates and installs a configuration snapshot.
//
// Validation collects every problem before writing anything: an operator who
// fixes a file should not have to play whack-a-mole with one error per upload.
// The confirm flag is checked first, because a missing confirmation must not
// trigger an expensive validation pass just to be refused at the end anyway.
func (s *Service) ImportConfig(snapshot model.ConfigSnapshot, confirm bool) (*model.ImportConfigResult, error) {
	if !confirm {
		return nil, invalid("confirm must be true: importing replaces all current configuration")
	}
	if snapshot.Version != model.ConfigSnapshotVersion {
		return nil, invalid("unsupported configuration version %d (expected %d)",
			snapshot.Version, model.ConfigSnapshotVersion)
	}

	problems := validateConfigSnapshot(snapshot)
	if len(problems) > 0 {
		return nil, &ValidationError{Message: "配置校验失败", Fields: fieldsFromProblems(problems)}
	}

	// Everything is sound: write in one transaction, then rebuild the runtime.
	admin, err := s.store.AdminByID(1)
	if err != nil {
		return nil, err
	}
	if err := s.store.ReplaceConfig(snapshot, admin.ID); err != nil {
		return nil, err
	}

	// Reload the in-memory settings from what was just written. The manager's
	// session signer keeps the existing secret, so only the settings the snapshot
	// carries change; a password/TOTP change invalidates existing sessions via
	// the session version, exactly like the dedicated endpoints do.
	if err := s.loadSettings(); err != nil {
		return nil, err
	}

	result := &model.ImportConfigResult{Warnings: []string{}}
	// Replace the runtime listeners with the imported set. Each inbound is
	// applied once, not once per client.
	inbounds, err := s.store.ListInbounds()
	if err != nil {
		return nil, err
	}
	for _, in := range inbounds {
		// RemoveInbound is safe when the listener was never started. It also
		// reclaims the accounting accounts of the previous configuration.
		s.mgr.RemoveInbound(in.ID)
		clients, err := s.store.ListClients(in.ID)
		if err != nil {
			return nil, err
		}
		if err := s.mgr.ApplyInbound(in, clients); err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("入站 %s (#%d) 已保存但未能启动：%v；请重启该入站", in.Remark, in.ID, err))
			continue
		}
		result.Reloads++
	}
	result.Inbounds = len(inbounds)
	for _, in := range inbounds {
		clients, err := s.store.ListClients(in.ID)
		if err != nil {
			return nil, err
		}
		result.Clients += len(clients)
	}
	return result, nil
}

// configProblem is one validation failure with the field path that caused it,
// so the panel can show "inbounds[2].port" rather than a prose paragraph.
type configProblem struct {
	field   string
	message string
}

func fieldsFromProblems(problems []configProblem) map[string]string {
	out := make(map[string]string, len(problems))
	for _, problem := range problems {
		out[problem.field] = problem.message
	}
	return out
}

// validateConfigSnapshot checks the whole file and returns every problem.
func validateConfigSnapshot(snapshot model.ConfigSnapshot) []configProblem {
	problems := make([]configProblem, 0)

	// Settings: reuse the same rules the settings endpoint applies.
	if snapshot.Settings.Timezone == "" {
		problems = append(problems, configProblem{"settings.timezone", "timezone is required"})
	} else if _, err := time.LoadLocation(snapshot.Settings.Timezone); err != nil {
		problems = append(problems, configProblem{"settings.timezone", "invalid timezone"})
	}
	if snapshot.Settings.PublicPort < 0 || snapshot.Settings.PublicPort > 65535 {
		problems = append(problems, configProblem{"settings.public_port", "must be between 0 and 65535"})
	}
	if snapshot.Settings.ResetDay < 0 || snapshot.Settings.ResetDay > 28 {
		problems = append(problems, configProblem{"settings.reset_day", "must be between 0 and 28"})
	}
	if snapshot.Settings.PublicHost != "" {
		host := normalizeHost(snapshot.Settings.PublicHost)
		if err := validateHostValue(host); err != nil {
			problems = append(problems, configProblem{"settings.public_host", err.Error()})
		}
	}

	// Administrator.
	if strings.TrimSpace(snapshot.Admin.Username) == "" {
		problems = append(problems, configProblem{"admin.username", "must not be empty"})
	}
	if !strings.HasPrefix(snapshot.Admin.PasswordHash, "$2a$") &&
		!strings.HasPrefix(snapshot.Admin.PasswordHash, "$2b$") {
		problems = append(problems, configProblem{"admin.password_hash", "must be a bcrypt hash"})
	}
	if snapshot.Admin.TOTPEnabled {
		if snapshot.Admin.TOTPSecret == "" {
			problems = append(problems, configProblem{"admin.totp_secret", "required when two-step verification is enabled"})
		}
		if len(snapshot.Admin.RecoveryCodeHashes) == 0 {
			problems = append(problems, configProblem{"admin.recovery_code_hashes", "at least one code is required"})
		}
	}

	// Inbounds, their ports and users.
	ports := make(map[int]bool, len(snapshot.Inbounds))
	tokens := make(map[string]bool)
	for i, in := range snapshot.Inbounds {
		prefix := fmt.Sprintf("inbounds[%d]", i)
		if in.Port < 1 || in.Port > 65535 {
			problems = append(problems, configProblem{prefix + ".port", "must be between 1 and 65535"})
		}
		if ports[in.Port] {
			problems = append(problems, configProblem{prefix + ".port", "duplicate port in snapshot"})
		}
		ports[in.Port] = true
		if !model.IsMethod(in.Method) {
			problems = append(problems, configProblem{prefix + ".method", "unsupported method"})
		} else if err := validateServerPSKForConfig(in.Method, in.ServerPSK); err != nil {
			problems = append(problems, configProblem{prefix + ".server_psk", err.Error()})
		}
		if in.DomainPort > 0 && in.Domain == "" {
			problems = append(problems, configProblem{prefix + ".domain_port", "requires a domain"})
		}

		names := make(map[string]bool, len(in.Clients))
		enabled := 0
		for j, c := range in.Clients {
			clientPrefix := fmt.Sprintf("%s.clients[%d]", prefix, j)
			if strings.TrimSpace(c.Name) == "" {
				problems = append(problems, configProblem{clientPrefix + ".name", "must not be empty"})
			}
			if names[c.Name] {
				problems = append(problems, configProblem{clientPrefix + ".name", "duplicate name in inbound"})
			}
			names[c.Name] = true
			if c.SubToken == "" {
				problems = append(problems, configProblem{clientPrefix + ".sub_token", "must not be empty"})
			}
			if tokens[c.SubToken] {
				problems = append(problems, configProblem{clientPrefix + ".sub_token", "duplicate token in snapshot"})
			}
			tokens[c.SubToken] = true
			if c.QuotaBytes < 0 || c.ExpiresAt < 0 || c.UpBytes < 0 || c.DownBytes < 0 || c.IPLimit < 0 {
				problems = append(problems, configProblem{clientPrefix, "quota, expiry, usage and ip_limit must not be negative"})
			}
			if model.IsMethod(in.Method) && !model.IsMultiUser(in.Method) && c.Enabled {
				enabled++
			}
		}
		if !model.IsMultiUser(in.Method) && enabled > 1 {
			problems = append(problems, configProblem{prefix + ".clients",
				"single-user methods allow at most one enabled client"})
		}
	}
	return problems
}

// validateServerPSKForConfig applies the same PSK rules the inbound form uses.
func validateServerPSKForConfig(method, psk string) error {
	// The same validator the create path uses, so a snapshot cannot install a
	// key the runtime would refuse to load.
	if err := ssserver.ValidateServerPSK(method, psk); err != nil {
		return err
	}
	return nil
}
