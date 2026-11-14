package store

import (
	"testing"

	"github.com/ydong/ssrservice/internal/model"
)

// insertAudit is a helper that fails the test on error.
func insertAudit(t *testing.T, st *Store, entry model.AuditLog) {
	t.Helper()
	if entry.CreatedAt == 0 {
		entry.CreatedAt = 1000
	}
	if err := st.InsertAudit(entry); err != nil {
		t.Fatalf("insert audit: %v", err)
	}
}

func TestAuditInsertAndFilter(t *testing.T) {
	st := newTestStore(t)
	entries := []model.AuditLog{
		{CreatedAt: 100, AdminID: 1, Username: "admin", Action: model.AuditAuthLogin, IP: "203.0.113.1"},
		{CreatedAt: 200, AdminID: 1, Username: "admin", Action: model.AuditClientCreate,
			TargetType: "client", TargetID: 7, Detail: `name="alice"`},
		{CreatedAt: 300, AdminID: 2, Username: "ops", Action: model.AuditClientUpdate,
			TargetType: "client", TargetID: 7, Detail: `name="alice" enabled=false`},
		{CreatedAt: 400, AdminID: 2, Username: "ops", Action: model.AuditSettingsUpdate},
	}
	for _, entry := range entries {
		insertAudit(t, st, entry)
	}

	all, total, err := st.ListAudit(AuditFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != int64(len(entries)) || len(all) != len(entries) {
		t.Fatalf("total=%d len=%d, want %d", total, len(all), len(entries))
	}
	// Newest first.
	if all[0].CreatedAt != 400 || all[len(all)-1].CreatedAt != 100 {
		t.Fatalf("unexpected order: %+v", all)
	}

	byAction, total, err := st.ListAudit(AuditFilter{Action: model.AuditClientCreate})
	if err != nil {
		t.Fatalf("filter action: %v", err)
	}
	if total != 1 || len(byAction) != 1 || byAction[0].TargetID != 7 {
		t.Fatalf("action filter returned %+v (total %d)", byAction, total)
	}

	byUser, total, err := st.ListAudit(AuditFilter{Username: "ops"})
	if err != nil {
		t.Fatalf("filter user: %v", err)
	}
	if total != 2 || len(byUser) != 2 {
		t.Fatalf("username filter returned %d rows (total %d)", len(byUser), total)
	}

	byTime, total, err := st.ListAudit(AuditFilter{Since: 200, Until: 400})
	if err != nil {
		t.Fatalf("filter time: %v", err)
	}
	if total != 2 || len(byTime) != 2 {
		t.Fatalf("time filter returned %+v (total %d)", byTime, total)
	}
	for _, entry := range byTime {
		if entry.CreatedAt < 200 || entry.CreatedAt >= 400 {
			t.Fatalf("time filter leaked %d", entry.CreatedAt)
		}
	}
}

func TestAuditPagingBounds(t *testing.T) {
	st := newTestStore(t)
	for i := 0; i < 250; i++ {
		insertAudit(t, st, model.AuditLog{CreatedAt: int64(i + 1), Username: "admin", Action: model.AuditAuthLogin})
	}

	page, total, err := st.ListAudit(AuditFilter{})
	if err != nil {
		t.Fatalf("default page: %v", err)
	}
	if total != 250 {
		t.Fatalf("total = %d, want 250", total)
	}
	if len(page) != AuditDefaultLimit {
		t.Fatalf("default limit page = %d rows, want %d", len(page), AuditDefaultLimit)
	}

	// A limit above the cap is clamped: with 250 rows available the page holds
	// all of them, and with more rows than the cap it would stop at AuditMaxLimit.
	capped, _, err := st.ListAudit(AuditFilter{Limit: 10000})
	if err != nil {
		t.Fatalf("capped page: %v", err)
	}
	if len(capped) != 250 {
		t.Fatalf("limit cap page = %d rows, want all 250", len(capped))
	}

	beyond, total, err := st.ListAudit(AuditFilter{Limit: 10, Offset: 1000})
	if err != nil {
		t.Fatalf("beyond page: %v", err)
	}
	if total != 250 || len(beyond) != 0 {
		t.Fatalf("offset beyond total: len=%d total=%d", len(beyond), total)
	}

	negative, _, err := st.ListAudit(AuditFilter{Limit: -5, Offset: -10})
	if err != nil {
		t.Fatalf("negative paging: %v", err)
	}
	if len(negative) != AuditDefaultLimit {
		t.Fatalf("negative limit page = %d rows, want default", len(negative))
	}
}

func TestAuditPruneAndTrim(t *testing.T) {
	st := newTestStore(t)
	for i := 0; i < 10; i++ {
		insertAudit(t, st, model.AuditLog{CreatedAt: int64(100 + i), Action: model.AuditAuthLogin})
	}
	removed, err := st.PruneAudit(105)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 5 {
		t.Fatalf("pruned %d rows, want 5", removed)
	}
	_, total, err := st.ListAudit(AuditFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 5 {
		t.Fatalf("remaining rows = %d, want 5", total)
	}

	trimmed, err := st.TrimAudit(2)
	if err != nil {
		t.Fatalf("trim: %v", err)
	}
	if trimmed != 3 {
		t.Fatalf("trimmed %d rows, want 3", trimmed)
	}
	page, total, err := st.ListAudit(AuditFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(page) != 2 {
		t.Fatalf("after trim total=%d len=%d, want 2", total, len(page))
	}
	// The newest rows must survive.
	if page[0].CreatedAt != 109 || page[1].CreatedAt != 108 {
		t.Fatalf("trim kept the wrong rows: %+v", page)
	}

	// Trimming below the cap is a no-op.
	if n, err := st.TrimAudit(100); err != nil || n != 0 {
		t.Fatalf("no-op trim removed %d rows (err %v)", n, err)
	}
}
