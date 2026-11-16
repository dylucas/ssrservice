package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/store"
)

// Batch client operations.
//
// The panel can select several users and act on them at once. Two rules shape the
// implementation:
//
//   - the work is grouped by inbound, and each inbound is written in one
//     transaction followed by exactly one reload. Reloading per user would
//     rebuild the protocol user table N times for one click, which is the cost
//     the bulk import path already avoids.
//   - a batch is partial-success like the import: the reply lists what was
//     applied and why the rest was not, instead of failing the whole request for
//     one ineligible row.

// Batch actions accepted by BatchClients.
const (
	BatchEnable   = "enable"
	BatchDisable  = "disable"
	BatchDelete   = "delete"
	BatchExtend   = "extend"
	BatchAddQuota = "add_quota"
	BatchReset    = "reset"
)

// BatchMaxClients bounds one batch. It matches the client page cap so "select
// all on this page" is always a single request.
const BatchMaxClients = 500

// BatchExtendMaxDays bounds one extension: ten years is already beyond any
// realistic plan, and the bound keeps a typo from overflowing the expiry field.
const BatchExtendMaxDays = 3650

// BatchAddQuotaMax bounds one quota increment (1 PiB). It is a sanity bound, not
// a business rule: unlimited accounts are refused separately.
const BatchAddQuotaMax = 1 << 50

// BatchFailure explains why one id was left alone.
type BatchFailure struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

// BatchResult is the reply of a batch operation.
type BatchResult struct {
	Action  string         `json:"action"`
	Applied int            `json:"applied"`
	Failed  []BatchFailure `json:"failed"`
}

// BatchRequest is one batch operation on a set of clients.
type BatchRequest struct {
	Action string  `json:"action"`
	IDs    []int64 `json:"ids"`
	// Days is the extension used by BatchExtend.
	Days int `json:"days"`
	// Bytes is the increment used by BatchAddQuota.
	Bytes int64 `json:"bytes"`
}

// NormalizeBatchRequest validates a batch request and returns a deduplicated set
// of ids.
//
// Invalid input is an error rather than a partially applied batch: a caller that
// sends an unknown action or 600 ids has a bug, and silently doing something else
// would hide it.
func NormalizeBatchRequest(req BatchRequest) (BatchRequest, error) {
	action := strings.ToLower(strings.TrimSpace(req.Action))
	switch action {
	case BatchEnable, BatchDisable, BatchDelete, BatchExtend, BatchAddQuota, BatchReset:
	default:
		return BatchRequest{}, invalid("action must be enable, disable, delete, extend, add_quota or reset")
	}
	seen := make(map[int64]struct{}, len(req.IDs))
	ids := make([]int64, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 {
			return BatchRequest{}, invalid("ids must be positive integers")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return BatchRequest{}, invalid("ids must not be empty")
	}
	if len(ids) > BatchMaxClients {
		return BatchRequest{}, invalid("too many ids: %d (limit %d)", len(ids), BatchMaxClients)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	switch action {
	case BatchExtend:
		if req.Days < 1 || req.Days > BatchExtendMaxDays {
			return BatchRequest{}, invalid("days must be between 1 and %d", BatchExtendMaxDays)
		}
	case BatchAddQuota:
		if req.Bytes < 1 || req.Bytes > BatchAddQuotaMax {
			return BatchRequest{}, invalid("bytes must be between 1 and %d", int64(BatchAddQuotaMax))
		}
	}
	return BatchRequest{Action: action, IDs: ids, Days: req.Days, Bytes: req.Bytes}, nil
}

// BatchClients applies one action to a set of clients.
//
// The identifiers are resolved first so unknown ones are reported instead of
// silently skipped, then the work is grouped by inbound. Each group is one
// transaction and one reload; a failure in one group does not roll back the
// others, because the operator selected a set, not a transaction.
func (s *Service) BatchClients(req BatchRequest) (*BatchResult, error) {
	normalized, err := NormalizeBatchRequest(req)
	if err != nil {
		return nil, err
	}
	refs, err := s.store.ListClientRefs(normalized.IDs)
	if err != nil {
		return nil, err
	}
	result := &BatchResult{Action: normalized.Action, Failed: []BatchFailure{}}

	known := make(map[int64]store.ClientRef, len(refs))
	for _, ref := range refs {
		known[ref.ID] = ref
	}
	// Report ids that do not exist instead of dropping them: a caller with a
	// stale page needs to know the selection was not fully applied.
	var byInbound = make(map[int64][]store.ClientRef)
	for _, id := range normalized.IDs {
		ref, ok := known[id]
		if !ok {
			result.Failed = append(result.Failed, BatchFailure{ID: id, Error: "user not found"})
			continue
		}
		byInbound[ref.InboundID] = append(byInbound[ref.InboundID], ref)
	}

	// Deterministic order: the reply and the audit entry should not depend on
	// map iteration.
	inboundIDs := make([]int64, 0, len(byInbound))
	for id := range byInbound {
		inboundIDs = append(inboundIDs, id)
	}
	sort.Slice(inboundIDs, func(i, j int) bool { return inboundIDs[i] < inboundIDs[j] })

	for _, inboundID := range inboundIDs {
		group := byInbound[inboundID]
		applied, failures, err := s.applyBatchGroup(normalized, inboundID, group)
		if err != nil {
			// A transaction or reload failure belongs to the whole group.
			for _, ref := range group {
				result.Failed = append(result.Failed, BatchFailure{ID: ref.ID, Name: ref.Name, Error: err.Error()})
			}
			continue
		}
		result.Applied += applied
		result.Failed = append(result.Failed, failures...)
	}
	return result, nil
}

// applyBatchGroup runs one action for the clients of one inbound.
func (s *Service) applyBatchGroup(req BatchRequest, inboundID int64, group []store.ClientRef) (applied int, failures []BatchFailure, err error) {
	switch req.Action {
	case BatchDelete:
		return s.batchDelete(inboundID, group)
	case BatchReset:
		return s.batchReset(group)
	}

	eligible := make([]int64, 0, len(group))
	failures = make([]BatchFailure, 0)
	for _, ref := range group {
		if reason := batchIneligible(req, ref); reason != "" {
			failures = append(failures, BatchFailure{ID: ref.ID, Name: ref.Name, Error: reason})
			continue
		}
		eligible = append(eligible, ref.ID)
	}
	if len(eligible) == 0 {
		return 0, failures, nil
	}

	if req.Action == BatchEnable {
		in, err := s.store.InboundByID(inboundID)
		if err != nil {
			return 0, failures, err
		}
		// The single-user rule is per inbound, and enabling a whole group at
		// once can break it in a way a per-row check would miss: an inbound can
		// end up with two enabled users even if each row was fine on its own.
		// Enabling every selected user is the intent, so the group is refused
		// here rather than half applied.
		if !model.IsMultiUser(in.Method) && len(eligible) > 1 {
			for _, id := range eligible {
				failures = append(failures, BatchFailure{ID: id, Name: refName(group, id),
					Error: "该入站为单用户方法，一次只能启用一个用户"})
			}
			return 0, failures, nil
		}
	}

	appliedIDs, err := s.store.ApplyClientBatch(eligible, batchUpdate(req))
	if err != nil {
		return 0, failures, err
	}
	// A row can disappear between the identity read and the update.
	appliedSet := make(map[int64]struct{}, len(appliedIDs))
	for _, id := range appliedIDs {
		appliedSet[id] = struct{}{}
	}
	for _, id := range eligible {
		if _, ok := appliedSet[id]; !ok {
			failures = append(failures, BatchFailure{ID: id, Name: refName(group, id),
				Error: "user disappeared before the update was applied"})
		}
	}

	// Disabling closes the live sessions of those users; the accounting follows
	// the row, so this has to happen after the commit.
	if req.Action == BatchDisable {
		for _, id := range appliedIDs {
			s.mgr.CloseClient(id)
		}
	}
	if err := s.reloadInbound(inboundID); err != nil {
		// The rows are committed, so the group is only partially consistent with
		// the runtime. Reporting it is better than pretending success, and a
		// restart of the inbound repairs it.
		return 0, failures, fmt.Errorf("saved, but the inbound failed to reload: %w", err)
	}
	return len(appliedIDs), failures, nil
}

// batchIneligible explains why one client cannot take part in the action.
func batchIneligible(req BatchRequest, ref store.ClientRef) string {
	switch req.Action {
	case BatchExtend:
		if ref.ExpiresAt == 0 {
			return "never expires, nothing to extend"
		}
	case BatchAddQuota:
		if ref.QuotaBytes == 0 {
			return "unlimited traffic, nothing to add"
		}
	}
	return ""
}

// batchUpdate turns an action into the store primitive that implements it.
func batchUpdate(req BatchRequest) store.BatchClientUpdate {
	update := store.BatchClientUpdate{}
	switch req.Action {
	case BatchEnable:
		enabled := true
		update.Enabled = &enabled
	case BatchDisable:
		enabled := false
		update.Enabled = &enabled
	case BatchExtend:
		update.ExtendDays = int64(req.Days)
	case BatchAddQuota:
		update.AddQuota = req.Bytes
	}
	return update
}

// batchDelete removes the group and reloads the inbound once.
func (s *Service) batchDelete(inboundID int64, group []store.ClientRef) (int, []BatchFailure, error) {
	ids := make([]int64, 0, len(group))
	for _, ref := range group {
		ids = append(ids, ref.ID)
	}
	deleted, err := s.store.DeleteClients(ids)
	if err != nil {
		return 0, nil, err
	}
	deletedSet := make(map[int64]struct{}, len(deleted))
	for _, id := range deleted {
		deletedSet[id] = struct{}{}
		// The runtime accounting account outlives the row and has to be
		// reclaimed, exactly like the single delete path does.
		s.mgr.RemoveClient(id)
	}
	failures := make([]BatchFailure, 0, len(ids)-len(deleted))
	for _, ref := range group {
		if _, ok := deletedSet[ref.ID]; !ok {
			failures = append(failures, BatchFailure{ID: ref.ID, Name: ref.Name,
				Error: "user disappeared before the delete was applied"})
		}
	}
	if err := s.reloadInbound(inboundID); err != nil {
		return 0, failures, fmt.Errorf("deleted, but the inbound failed to reload: %w", err)
	}
	return len(deleted), failures, nil
}

// batchReset zeroes the traffic of each selected client.
//
// This is the one action that cannot be a single SQL statement: the in-memory
// counters live behind the runtime's own lock, and zeroing only the row would let
// the next flush write the old counters straight back. ResetClient already
// performs exactly that dance, so the batch reuses it per user.
func (s *Service) batchReset(group []store.ClientRef) (int, []BatchFailure, error) {
	applied := 0
	failures := make([]BatchFailure, 0)
	var firstErr error
	for _, ref := range group {
		if err := s.ResetClient(ref.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				failures = append(failures, BatchFailure{ID: ref.ID, Name: ref.Name, Error: "user not found"})
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			failures = append(failures, BatchFailure{ID: ref.ID, Name: ref.Name, Error: err.Error()})
			continue
		}
		applied++
	}
	return applied, failures, firstErr
}

// refName looks a name up in the group, for a failure row.
func refName(group []store.ClientRef, id int64) string {
	for _, ref := range group {
		if ref.ID == id {
			return ref.Name
		}
	}
	return ""
}
