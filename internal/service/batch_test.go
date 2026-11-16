package service

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/store"
)

// newBatchEnv builds a service with two inbounds and clients on each, which is
// what the grouping rules have to be tested against.
func newBatchEnv(t *testing.T, method string) (*Service, *store.Store, []model.Client) {
	t.Helper()
	svc, st := newAccessEnv(t)
	first := model.Inbound{Remark: "first", Listen: "127.0.0.1", Port: 28201,
		Method: method, Enabled: true, UDPEnabled: true}
	if model.IsMultiUser(method) {
		// A single-user method rejects a server PSK at listener build time.
		first.ServerPSK = mustServerPSK(t, method)
	}
	if err := st.CreateInbound(&first); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	secondMethod := model.Method2022AES128GCM
	second := model.Inbound{Remark: "second", Listen: "127.0.0.1", Port: 28202,
		Method: secondMethod, ServerPSK: mustServerPSK(t, secondMethod), Enabled: true, UDPEnabled: true}
	if err := st.CreateInbound(&second); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	clients := make([]model.Client, 0, 4)
	for i, name := range []string{"alice", "bob"} {
		c := model.Client{InboundID: first.ID, Name: name, PSK: clientKeyFor(t, method, i),
			Enabled: true, SubToken: "tok-" + name, QuotaBytes: 100, ExpiresAt: time.Now().Add(72 * time.Hour).Unix()}
		if err := st.CreateClient(&c); err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
		clients = append(clients, c)
	}
	for i, name := range []string{"carol", "dave"} {
		c := model.Client{InboundID: second.ID, Name: name, PSK: clientKeyFor(t, secondMethod, i),
			Enabled: true, SubToken: "tok-" + name, QuotaBytes: 100}
		if err := st.CreateClient(&c); err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
		clients = append(clients, c)
	}
	return svc, st, clients
}

func clientIDs(clients ...model.Client) []int64 {
	ids := make([]int64, 0, len(clients))
	for _, c := range clients {
		ids = append(ids, c.ID)
	}
	return ids
}

// TestBatchDisableEnablesAcrossInboundsAndReloadsOnce is the core guarantee:
// the rows change and each inbound is reloaded exactly once, no matter how many
// users were selected on it.
func TestBatchDisableEnablesAcrossInboundsAndReloadsOnce(t *testing.T) {
	svc, st, clients := newBatchEnv(t, model.Method2022AES128GCM)

	before := svc.ReloadCount()
	result, err := svc.BatchClients(BatchRequest{Action: BatchDisable, IDs: clientIDs(clients...)})
	if err != nil {
		t.Fatalf("batch disable: %v", err)
	}
	if result.Applied != 4 || len(result.Failed) != 0 {
		t.Fatalf("unexpected result %+v", result)
	}
	if reloads := svc.ReloadCount() - before; reloads != 2 {
		t.Fatalf("reloaded %d times, want once per inbound (2)", reloads)
	}
	for _, c := range clients {
		stored, err := st.ClientByID(c.ID)
		if err != nil {
			t.Fatalf("load %s: %v", c.Name, err)
		}
		if stored.Enabled {
			t.Fatalf("%s is still enabled", c.Name)
		}
	}

	// And back on again, with the same one-reload-per-inbound rule.
	before = svc.ReloadCount()
	result, err = svc.BatchClients(BatchRequest{Action: BatchEnable, IDs: clientIDs(clients...)})
	if err != nil {
		t.Fatalf("batch enable: %v", err)
	}
	if result.Applied != 4 {
		t.Fatalf("unexpected result %+v", result)
	}
	if reloads := svc.ReloadCount() - before; reloads != 2 {
		t.Fatalf("reloaded %d times on enable, want 2", reloads)
	}
}

// TestBatchEnableRefusesSingleUserInbound covers the rule that must not be
// bypassed by a bulk path: a single-user method cannot serve two enabled users.
func TestBatchEnableRefusesSingleUserInbound(t *testing.T) {
	svc, st, clients := newBatchEnv(t, model.Method2022ChaCha20)
	// Keep only one user enabled on the single-user inbound, as the create path
	// would have enforced.
	if err := st.SetClientEnabled(clients[1].ID, false); err != nil {
		t.Fatalf("disable bob: %v", err)
	}

	result, err := svc.BatchClients(BatchRequest{Action: BatchEnable, IDs: clientIDs(clients[0], clients[1])})
	if err != nil {
		t.Fatalf("batch enable: %v", err)
	}
	if result.Applied != 0 {
		t.Fatalf("the single-user inbound was enabled anyway: %+v", result)
	}
	if len(result.Failed) != 2 {
		t.Fatalf("expected both ids to fail: %+v", result.Failed)
	}
	for _, failure := range result.Failed {
		if !strings.Contains(failure.Error, "单用户") {
			t.Fatalf("unexpected failure %+v", failure)
		}
	}
	// The other inbound in the same batch is multi-user and still applies.
	result, err = svc.BatchClients(BatchRequest{Action: BatchEnable,
		IDs: []int64{clients[0].ID, clients[1].ID, clients[2].ID}})
	if err != nil {
		t.Fatalf("batch enable: %v", err)
	}
	if result.Applied != 1 || len(result.Failed) != 2 {
		t.Fatalf("grouping leaked across inbounds: %+v", result)
	}
}

// TestBatchExtendAndAddQuota covers the two numeric actions and their refusals.
func TestBatchExtendAndAddQuota(t *testing.T) {
	svc, st, clients := newBatchEnv(t, model.Method2022AES128GCM)
	expiring := clients[0]
	// A permanent account has nothing to extend; an unlimited one nothing to add.
	permanent := clients[1]
	if err := st.UpdateClient(&model.Client{ID: permanent.ID, InboundID: permanent.InboundID,
		Name: permanent.Name, PSK: permanent.PSK, SubToken: permanent.SubToken,
		Enabled: true, QuotaBytes: 0, ExpiresAt: 0}); err != nil {
		t.Fatalf("make permanent: %v", err)
	}

	result, err := svc.BatchClients(BatchRequest{Action: BatchExtend, Days: 30,
		IDs: clientIDs(expiring, permanent)})
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if result.Applied != 1 || len(result.Failed) != 1 {
		t.Fatalf("unexpected extend result %+v", result)
	}
	if !strings.Contains(result.Failed[0].Error, "never expires") {
		t.Fatalf("unexpected failure %+v", result.Failed[0])
	}
	stored, err := st.ClientByID(expiring.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := expiring.ExpiresAt + 30*24*60*60
	if stored.ExpiresAt != want {
		t.Fatalf("expires_at = %d, want %d", stored.ExpiresAt, want)
	}

	result, err = svc.BatchClients(BatchRequest{Action: BatchAddQuota, Bytes: 1024,
		IDs: clientIDs(expiring, permanent)})
	if err != nil {
		t.Fatalf("add quota: %v", err)
	}
	if result.Applied != 1 || len(result.Failed) != 1 {
		t.Fatalf("unexpected add-quota result %+v", result)
	}
	stored, err = st.ClientByID(expiring.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stored.QuotaBytes != expiring.QuotaBytes+1024 {
		t.Fatalf("quota = %d, want %d", stored.QuotaBytes, expiring.QuotaBytes+1024)
	}
}

// TestBatchDeleteAndUnknownIDs covers deletion plus the partial-success reply for
// identifiers that do not exist.
func TestBatchDeleteAndUnknownIDs(t *testing.T) {
	svc, st, clients := newBatchEnv(t, model.Method2022AES128GCM)
	ids := append(clientIDs(clients[0], clients[2]), 999999)

	result, err := svc.BatchClients(BatchRequest{Action: BatchDelete, IDs: ids})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if result.Applied != 2 {
		t.Fatalf("applied = %d, want 2: %+v", result.Applied, result)
	}
	if len(result.Failed) != 1 || result.Failed[0].ID != 999999 {
		t.Fatalf("unknown id was not reported: %+v", result.Failed)
	}
	for _, id := range []int64{clients[0].ID, clients[2].ID} {
		if _, err := st.ClientByID(id); err == nil {
			t.Fatalf("client %d survived the delete", id)
		}
	}
	if _, err := st.ClientByID(clients[1].ID); err != nil {
		t.Fatalf("an unselected client was deleted: %v", err)
	}
}

// TestBatchResetZeroesCounters covers the one action that goes through the
// per-user runtime path.
func TestBatchResetZeroesCounters(t *testing.T) {
	svc, st, clients := newBatchEnv(t, model.Method2022AES128GCM)
	target := clients[0]
	target.UpBytes, target.DownBytes = 500, 700
	if err := st.UpdateClient(&target); err != nil {
		t.Fatalf("seed usage: %v", err)
	}

	result, err := svc.BatchClients(BatchRequest{Action: BatchReset, IDs: clientIDs(target)})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if result.Applied != 1 || len(result.Failed) != 0 {
		t.Fatalf("unexpected result %+v", result)
	}
	stored, err := st.ClientByID(target.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stored.UpBytes != 0 || stored.DownBytes != 0 {
		t.Fatalf("counters not reset: %+v", stored)
	}
	if stored.LastResetAt == 0 {
		t.Fatal("reset did not stamp last_reset_at")
	}
}

// TestNormalizeBatchRequest covers the validation that keeps a bad request from
// silently doing something else.
func TestNormalizeBatchRequest(t *testing.T) {
	good, err := NormalizeBatchRequest(BatchRequest{Action: "ENABLE", IDs: []int64{3, 1, 3}})
	if err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if good.Action != BatchEnable || len(good.IDs) != 2 || good.IDs[0] != 1 || good.IDs[1] != 3 {
		t.Fatalf("normalized = %+v", good)
	}

	bad := []BatchRequest{
		{Action: "explode", IDs: []int64{1}},
		{Action: BatchEnable},
		{Action: BatchEnable, IDs: []int64{0}},
		{Action: BatchEnable, IDs: []int64{-3}},
		{Action: BatchExtend, IDs: []int64{1}, Days: 0},
		{Action: BatchExtend, IDs: []int64{1}, Days: 40000},
		{Action: BatchAddQuota, IDs: []int64{1}, Bytes: 0},
		{Action: BatchAddQuota, IDs: []int64{1}, Bytes: BatchAddQuotaMax + 1},
	}
	for _, req := range bad {
		if _, err := NormalizeBatchRequest(req); err == nil {
			t.Fatalf("accepted %+v", req)
		}
	}

	// The size cap is what keeps one request from rebuilding a listener for an
	// unbounded number of clients.
	tooMany := make([]int64, BatchMaxClients+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	if _, err := NormalizeBatchRequest(BatchRequest{Action: BatchEnable, IDs: tooMany}); err == nil {
		t.Fatal("accepted a batch over the cap")
	}
}

// clientKeyFor generates a client key of the length the method requires. A
// placeholder string would make the listener fail to build on reload, which is
// not what these tests are about.
func clientKeyFor(t *testing.T, method string, index int) string {
	t.Helper()
	size, ok := model.MethodKeySize[method]
	if !ok || size == 0 {
		t.Fatalf("method %q has no PSK length", method)
	}
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte('A' + (index+i)%26)
	}
	return base64.StdEncoding.EncodeToString(buf)
}
