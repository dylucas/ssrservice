package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

// Attention window and the list-only status values.
//
// The dashboard's "需要关注" cards and the list filter that the card links to have
// to answer the same question, otherwise clicking a card shows a different
// number. Both therefore share the SQL predicates below, and a test walks every
// branch to fail on drift.
const (
	// AttentionExpiringDays is how far ahead "expires soon" looks. A week is
	// the window an operator can still act in (renew, extend, warn).
	AttentionExpiringDays = 7

	// StatusExpiring and StatusIdle are list filter values, not statuses:
	// model.Client.StatusAt never returns them. They exist so the dashboard can
	// link to the exact rows behind each of its attention cards.
	StatusExpiring = "expiring"
	StatusIdle     = "idle"
)

// expiringClause selects enabled clients that expire within the window. Both
// bounds are exclusive/inclusive exactly the way "expires in N days" is phrased
// elsewhere: a client that expires exactly now is expired, not expiring.
func expiringClause(now int64) (string, []any) {
	return `c.enabled = 1 AND c.expires_at > ? AND c.expires_at <= ?`,
		[]any{now, now + AttentionExpiringDays*24*60*60}
}

// idleClause selects enabled clients that have never moved any traffic.
func idleClause() string {
	return `c.enabled = 1 AND c.up_bytes + c.down_bytes = 0`
}

// AttentionCounts is the dashboard's "need attention" summary.
type AttentionCounts struct {
	// ExpiringSoon is active and expiring within AttentionExpiringDays.
	ExpiringSoon int64
	// Expired is enabled with an expiry in the past.
	Expired int64
	// QuotaExceeded is enabled and over quota.
	QuotaExceeded int64
	// Idle is enabled and has never moved any traffic.
	Idle int64
	// WindowDays is the "expires soon" window the count used, so the panel can
	// describe it without duplicating the constant.
	WindowDays int
}

// Attention aggregates the four groups in one query.
//
// The first three predicates come from clientStatusClause, so the numbers here
// and the rows the list shows for the same status cannot disagree. Idle is the
// only group without a matching computed status, which is why it has a filter
// value of its own.
func (s *Store) Attention(now int64) (AttentionCounts, error) {
	statusExpired, expiredArgs, err := clientStatusClause(model.StatusExpired, now)
	if err != nil {
		return AttentionCounts{}, err
	}
	statusQuota, quotaArgs, err := clientStatusClause(model.StatusQuotaExceeded, now)
	if err != nil {
		return AttentionCounts{}, err
	}
	expiring, expiringArgs := expiringClause(now)

	// Each branch repeats the FROM/JOIN so the arguments line up with their own
	// subquery; the counts are independent, not a single grouped scan.
	query := `SELECT
		(SELECT COUNT(*) FROM clients c JOIN inbounds i ON i.id = c.inbound_id WHERE ` + expiring + `),
		(SELECT COUNT(*) FROM clients c JOIN inbounds i ON i.id = c.inbound_id WHERE ` + statusExpired + `),
		(SELECT COUNT(*) FROM clients c JOIN inbounds i ON i.id = c.inbound_id WHERE ` + statusQuota + `),
		(SELECT COUNT(*) FROM clients c JOIN inbounds i ON i.id = c.inbound_id WHERE ` + idleClause() + `)`

	args := make([]any, 0, 8)
	args = append(args, expiringArgs...)
	args = append(args, expiredArgs...)
	args = append(args, quotaArgs...)

	counts := AttentionCounts{WindowDays: AttentionExpiringDays}
	if err := s.db.QueryRow(query, args...).Scan(&counts.ExpiringSoon, &counts.Expired,
		&counts.QuotaExceeded, &counts.Idle); err != nil {
		return AttentionCounts{}, fmt.Errorf("attention counts: %w", err)
	}
	return counts, nil
}

// BatchClientUpdate is one server-side change applied to a set of clients.
//
// Every field is optional; the caller sets the ones its action needs. The store
// only offers the primitives: which client may be changed, and whether the
// change is allowed, is the service's decision.
type BatchClientUpdate struct {
	Enabled *bool
	// ExtendDays pushes expires_at forward, and ClearExpiry is set instead when
	// the client never expires (the service refuses that case, see BatchClients).
	ExtendDays int64
	// AddQuota adds to quota_bytes; 0 means "no quota change".
	AddQuota int64
}

// ApplyClientBatch applies one update to the given clients in a single
// transaction and returns the ids that existed. It does not touch the runtime:
// the service decides when to reload the listener.
func (s *Store) ApplyClientBatch(ids []int64, update BatchClientUpdate) (applied []int64, err error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// The placeholder list is built once so the read and the write share it.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+3)
	for _, id := range ids {
		args = append(args, id)
	}

	err = s.write(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM clients WHERE id IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			applied = append(applied, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(applied) == 0 {
			return nil
		}

		exists := strings.TrimSuffix(strings.Repeat("?,", len(applied)), ",")
		writeArgs := make([]any, 0, len(applied)+3)
		set := make([]string, 0, 4)
		if update.Enabled != nil {
			set = append(set, "enabled = ?")
			writeArgs = append(writeArgs, boolInt(*update.Enabled))
		}
		if update.ExtendDays > 0 {
			// A day is 86400 seconds here: the expiry field is an instant, and
			// adding days to it is exact in absolute time even across a
			// daylight-saving change (the same convention the panel uses).
			set = append(set, "expires_at = expires_at + ?")
			writeArgs = append(writeArgs, update.ExtendDays*24*60*60)
		}
		if update.AddQuota > 0 {
			set = append(set, "quota_bytes = quota_bytes + ?")
			writeArgs = append(writeArgs, update.AddQuota)
		}
		if len(set) == 0 {
			return nil
		}
		set = append(set, "updated_at = ?")
		writeArgs = append(writeArgs, time.Now().Unix())
		for _, id := range applied {
			writeArgs = append(writeArgs, id)
		}
		if _, err := tx.Exec(`UPDATE clients SET `+strings.Join(set, ", ")+
			` WHERE id IN (`+exists+`)`, writeArgs...); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applied, nil
}

// DeleteClients removes several clients in one transaction.
//
// The traffic detail rows cascade with the client, but the per-day rollup does
// not, so the affected days are collected first and rebuilt afterwards inside
// the same transaction (the same rule DeleteClient follows).
func (s *Store) DeleteClients(ids []int64) (deleted []int64, err error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	err = s.write(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, inbound_id FROM clients WHERE id IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		var found []int64
		for rows.Next() {
			var id, inboundID int64
			if err := rows.Scan(&id, &inboundID); err != nil {
				rows.Close()
				return err
			}
			found = append(found, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(found) == 0 {
			return nil
		}
		days, err := dayKeysForClients(tx, found)
		if err != nil {
			return err
		}
		exists := strings.TrimSuffix(strings.Repeat("?,", len(found)), ",")
		deleteArgs := make([]any, 0, len(found))
		for _, id := range found {
			deleteArgs = append(deleteArgs, id)
		}
		if _, err := tx.Exec(`DELETE FROM clients WHERE id IN (`+exists+`)`, deleteArgs...); err != nil {
			return err
		}
		if err := refreshDailyTotals(tx, days); err != nil {
			return err
		}
		deleted = found
		return nil
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

// ClientRef is the minimal identity of a client, used by the batch path to group
// work by inbound without reading credentials.
type ClientRef struct {
	ID        int64
	InboundID int64
	Name      string
	Enabled   bool
	// QuotaBytes and ExpiresAt let the service refuse an operation that has no
	// effect (adding quota to an unlimited account, extending a permanent one).
	QuotaBytes int64
	ExpiresAt  int64
}

// ListClientRefs returns the identity rows of the given clients. Unknown ids are
// simply absent; the caller reports them as failures.
func (s *Store) ListClientRefs(ids []int64) ([]ClientRef, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT id, inbound_id, name, enabled, quota_bytes, expires_at
		FROM clients WHERE id IN (`+placeholders+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make([]ClientRef, 0, len(ids))
	for rows.Next() {
		var (
			ref     ClientRef
			enabled int64
		)
		if err := rows.Scan(&ref.ID, &ref.InboundID, &ref.Name, &enabled,
			&ref.QuotaBytes, &ref.ExpiresAt); err != nil {
			return nil, err
		}
		ref.Enabled = enabled != 0
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}
