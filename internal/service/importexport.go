package service

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/subscription"
)

// maxImportLines bounds one import request. The panel is a single-operator tool,
// so a 1000 line batch is already generous and keeps a bad paste from creating
// an unbounded amount of work under the store write lock.
const maxImportLines = 1000

// Import formats accepted by ImportClients.
const (
	ImportFormatAuto = "auto"
	ImportFormatCSV  = "csv"
	ImportFormatSS   = "ss"
)

// ImportDefaults are applied to rows that do not specify a value.
type ImportDefaults struct {
	Enabled     *bool  `json:"enabled"`
	QuotaBytes  int64  `json:"quota_bytes"`
	ExpiresAt   int64  `json:"expires_at"`
	IPLimit     int    `json:"ip_limit"`
	ResetPolicy string `json:"reset_policy"`
	ResetDay    int    `json:"reset_day"`
}

// ImportRequest is the body of the import endpoint.
type ImportRequest struct {
	Format   string          `json:"format"`
	Content  string          `json:"content"`
	Defaults *ImportDefaults `json:"defaults"`
}

// ImportFailure describes one rejected line.
type ImportFailure struct {
	Line  int    `json:"line"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

// ImportResult is the outcome of an import. Partial success is a normal result:
// the created users stay, and the failures explain what was skipped.
type ImportResult struct {
	Created int             `json:"created"`
	Skipped int             `json:"skipped"`
	Failed  []ImportFailure `json:"failed"`
}

// importCandidate is one parsed line before validation.
type importCandidate struct {
	line int
	name string
	psk  string
	// client carries the per-line settings, nil entries fall back to defaults.
	quota     *int64
	expiresAt *int64
	ipLimit   *int
	reset     *string
	resetDay  *int
	note      string
	enabled   *bool
}

// importRow pairs a validated client with the line it came from, so a rejected
// chunk can be retried row by row and still report line numbers.
type importRow struct {
	client model.Client
	line   int
}

// ImportClients creates users in one inbound from CSV or ss:// links.
//
// The whole batch is validated up front and then inserted in bulk: the created
// users go in through a handful of statements inside one transaction, one token
// per user is allocated with a couple of queries instead of one per row, and the
// inbound is reloaded once at the end. Doing any of that per row is what made a
// 999 line import take 119ms.
//
// Partial success stays a normal result: the created users remain, and each
// rejected line keeps its own message and line number.
func (s *Service) ImportClients(inboundID int64, req ImportRequest) (*ImportResult, error) {
	in, err := s.store.InboundByID(inboundID)
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return nil, invalid("content must not be empty")
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = ImportFormatAuto
	}
	switch format {
	case ImportFormatAuto, ImportFormatCSV, ImportFormatSS:
	default:
		return nil, invalid("format must be auto, csv or ss")
	}

	lines := splitImportLines(content)
	if len(lines) == 0 {
		return nil, invalid("content has no rows")
	}
	if len(lines) > maxImportLines {
		return nil, invalid("too many lines: %d (limit %d)", len(lines), maxImportLines)
	}
	if format == ImportFormatAuto {
		format = detectImportFormat(lines)
	}

	var (
		candidates []importCandidate
		result     = &ImportResult{Failed: []ImportFailure{}}
	)
	switch format {
	case ImportFormatCSV:
		candidates, result.Failed = parseCSVImport(lines)
	case ImportFormatSS:
		candidates, result.Failed = parseSSImport(lines)
	}

	// Existing names are rejected per line, matching the create endpoint, which
	// relies on the (inbound_id, name) uniqueness constraint. Only the identity
	// columns are read: the import never needs the PSKs of existing users.
	existing, err := s.store.ListClientIdentities(inboundID)
	if err != nil {
		return nil, err
	}
	taken := make(map[string]struct{}, len(existing)+len(candidates))
	enabledExisting := 0
	for _, identity := range existing {
		taken[identity.Name] = struct{}{}
		if identity.Enabled {
			enabledExisting++
		}
	}
	singleUser := !model.IsMultiUser(in.Method)

	// Validate every line before writing anything. `taken` and the enabled
	// counter are updated as lines are accepted, so a duplicate inside the batch
	// or a second enabled user on a single-user method is caught without a
	// second query per line.
	planned := make([]importRow, 0, len(candidates))
	for _, candidate := range candidates {
		client := s.importCandidateToClient(in, candidate, req.Defaults)
		client.InboundID = inboundID
		if err := validateClientFields(in, &client); err != nil {
			result.Failed = append(result.Failed, ImportFailure{
				Line: candidate.line, Name: candidate.name, Error: err.Error(),
			})
			continue
		}
		if _, ok := taken[client.Name]; ok {
			result.Failed = append(result.Failed, ImportFailure{
				Line: candidate.line, Name: client.Name, Error: "name already exists in this inbound",
			})
			continue
		}
		if singleUser && client.Enabled && enabledExisting > 0 {
			result.Failed = append(result.Failed, ImportFailure{
				Line: candidate.line, Name: client.Name,
				Error: fmt.Sprintf("method %s only supports a single enabled client per inbound", in.Method),
			})
			continue
		}
		taken[client.Name] = struct{}{}
		if client.Enabled {
			enabledExisting++
		}
		if client.PSK == "" {
			psk, err := ssserver.GenerateClientPSK(in.Method)
			if err != nil {
				return nil, err
			}
			client.PSK = psk
		}
		planned = append(planned, importRow{client: client, line: candidate.line})
	}

	tokens, err := s.uniqueTokens(len(planned))
	if err != nil {
		return nil, err
	}
	now := model.NowUnix()
	for i := range planned {
		planned[i].client.SubToken = tokens[i]
		// Start the monthly reset clock at creation, as the create endpoint does.
		planned[i].client.LastResetAt = now
	}

	created, insertFailures := s.insertImportRows(planned)
	result.Failed = append(result.Failed, insertFailures...)

	if created > 0 {
		if err := s.reloadInbound(inboundID); err != nil {
			// The rows are committed; report the reload failure instead of
			// pretending nothing happened. Any later inbound change reloads.
			return nil, fmt.Errorf("imported %d clients but reloading the inbound failed: %w", created, err)
		}
	}
	result.Created = created
	return result, nil
}

// insertImportRows stores the validated batch in bulk.
//
// The normal path is one transaction with chunked multi-row inserts. A chunk the
// database rejects as a whole (a name inserted concurrently by another request
// is the realistic case) is retried row by row, so every rejected line still
// keeps its own message and line number and only that line is lost.
func (s *Service) insertImportRows(rows []importRow) (int, []ImportFailure) {
	if len(rows) == 0 {
		return 0, nil
	}
	clients := make([]*model.Client, len(rows))
	for i := range rows {
		clients[i] = &rows[i].client
	}
	if err := s.store.CreateClients(clients); err == nil {
		return len(rows), nil
	}
	// The batch is atomic, so nothing was written: retry one row at a time to
	// attribute the failure.
	failed := make([]ImportFailure, 0, 1)
	created := 0
	for i := range rows {
		if err := s.store.CreateClient(&rows[i].client); err != nil {
			failed = append(failed, ImportFailure{
				Line: rows[i].line, Name: rows[i].client.Name, Error: err.Error(),
			})
			continue
		}
		created++
	}
	return created, failed
}

// importCandidateToClient turns a parsed line into a client, applying defaults.
func (s *Service) importCandidateToClient(in *model.Inbound, candidate importCandidate, defaults *ImportDefaults) model.Client {
	enabled := true
	client := model.Client{Name: candidate.name, PSK: candidate.psk, Note: candidate.note,
		Enabled: enabled, ResetPolicy: model.ResetNone}
	if defaults != nil {
		if defaults.Enabled != nil {
			client.Enabled = *defaults.Enabled
		}
		client.QuotaBytes = defaults.QuotaBytes
		client.ExpiresAt = defaults.ExpiresAt
		client.IPLimit = defaults.IPLimit
		if defaults.ResetPolicy != "" {
			client.ResetPolicy = defaults.ResetPolicy
		}
		client.ResetDay = defaults.ResetDay
	}
	if candidate.enabled != nil {
		client.Enabled = *candidate.enabled
	}
	if candidate.quota != nil {
		client.QuotaBytes = *candidate.quota
	}
	if candidate.expiresAt != nil {
		client.ExpiresAt = *candidate.expiresAt
	}
	if candidate.ipLimit != nil {
		client.IPLimit = *candidate.ipLimit
	}
	if candidate.reset != nil {
		client.ResetPolicy = *candidate.reset
	}
	if candidate.resetDay != nil {
		client.ResetDay = *candidate.resetDay
	}
	return client
}

// splitImportLines splits on newlines, dropping blank lines and surrounding
// whitespace. Line numbers stay 1 based so failures point at the pasted text.
func splitImportLines(content string) []string {
	raw := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// detectImportFormat picks CSV when the first line looks like a header or holds
// commas, and ss:// otherwise.
func detectImportFormat(lines []string) string {
	if len(lines) == 0 {
		return ImportFormatCSV
	}
	first := lines[0]
	if strings.HasPrefix(first, "ss://") {
		return ImportFormatSS
	}
	if strings.Contains(first, ",") {
		return ImportFormatCSV
	}
	// A bare list of links without ss:// would be a subscription blob; treat it
	// as CSV so the failure message is about the columns rather than the scheme.
	return ImportFormatCSV
}

// csvColumns is the column order used when the file has no header.
var csvColumns = []string{
	"name", "psk", "enabled", "quota_bytes", "expires_at", "ip_limit", "reset_policy", "reset_day", "note",
}

// parseCSVImport parses CSV rows. It returns the candidates and the per-line
// failures; whole-file parse errors are reported as a single failure.
func parseCSVImport(lines []string) ([]importCandidate, []ImportFailure) {
	failed := make([]ImportFailure, 0)
	reader := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, append(failed, ImportFailure{Error: "invalid CSV: " + err.Error()})
	}
	if len(records) == 0 {
		return nil, failed
	}

	// Map header names to column indexes when the first row looks like a header.
	index := make(map[string]int, len(csvColumns))
	start := 0
	if hasCSVHeader(records[0]) {
		for i, name := range records[0] {
			index[strings.ToLower(strings.TrimSpace(name))] = i
		}
		start = 1
	} else {
		for i, name := range csvColumns {
			index[name] = i
		}
	}

	candidates := make([]importCandidate, 0, len(records))
	for i := start; i < len(records); i++ {
		record := records[i]
		lineNumber := i + 1
		cell := func(column string) string {
			at, ok := index[column]
			if !ok || at >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[at])
		}
		name := cell("name")
		if name == "" {
			failed = append(failed, ImportFailure{Line: lineNumber, Error: "name is required"})
			continue
		}
		candidate := importCandidate{line: lineNumber, name: name, psk: cell("psk"), note: cell("note")}
		if value := cell("enabled"); value != "" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				failed = append(failed, ImportFailure{Line: lineNumber, Name: name, Error: "enabled must be true or false"})
				continue
			}
			candidate.enabled = &parsed
		}
		if value := cell("quota_bytes"); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed < 0 {
				failed = append(failed, ImportFailure{Line: lineNumber, Name: name, Error: "quota_bytes must be a non-negative integer"})
				continue
			}
			candidate.quota = &parsed
		}
		if value := cell("expires_at"); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed < 0 {
				failed = append(failed, ImportFailure{Line: lineNumber, Name: name, Error: "expires_at must be a non-negative unix timestamp"})
				continue
			}
			candidate.expiresAt = &parsed
		}
		if value := cell("ip_limit"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				failed = append(failed, ImportFailure{Line: lineNumber, Name: name, Error: "ip_limit must be a non-negative integer"})
				continue
			}
			candidate.ipLimit = &parsed
		}
		if value := cell("reset_policy"); value != "" {
			candidate.reset = &value
		}
		if value := cell("reset_day"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				failed = append(failed, ImportFailure{Line: lineNumber, Name: name, Error: "reset_day must be a non-negative integer"})
				continue
			}
			candidate.resetDay = &parsed
		}
		candidates = append(candidates, candidate)
	}
	return candidates, failed
}

// hasCSVHeader reports whether the first record names the known columns.
//
// It looks for the "name" column anywhere in the row rather than assuming it is
// first: the exported file starts with inbound_id and inbound_remark, and must
// import back into another inbound unchanged.
func hasCSVHeader(record []string) bool {
	for _, cell := range record {
		if strings.EqualFold(strings.TrimSpace(cell), "name") {
			return true
		}
	}
	return false
}

// parseSSImport parses ss:// links, one per line.
func parseSSImport(lines []string) ([]importCandidate, []ImportFailure) {
	failed := make([]ImportFailure, 0)
	candidates := make([]importCandidate, 0, len(lines))
	for i, line := range lines {
		lineNumber := i + 1
		proxy, err := subscription.ParseLink(line)
		if err != nil {
			failed = append(failed, ImportFailure{Line: lineNumber, Error: err.Error()})
			continue
		}
		name := proxy.Name
		if name == "" {
			name = fmt.Sprintf("imported-%d", lineNumber)
		}
		candidates = append(candidates, importCandidate{line: lineNumber, name: name, psk: proxy.ClientPSK})
	}
	return candidates, failed
}

// ExportClientsCSV renders the clients of one inbound (0 = all) as CSV.
//
// The export contains the client PSKs: without them the file cannot restore the
// users. It is a same-privilege operation as GET /clients/:id, the response is
// marked no-store, and the panel access log records only the path.
func (s *Service) ExportClientsCSV(inboundID int64) ([]byte, error) {
	clients, err := s.store.ListClients(inboundID)
	if err != nil {
		return nil, err
	}
	inbounds, err := s.store.ListInbounds()
	if err != nil {
		return nil, err
	}
	remarks := make(map[int64]string, len(inbounds))
	for _, in := range inbounds {
		remarks[in.ID] = in.Remark
	}

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	// The column set is what the importer understands, plus two informational
	// columns (inbound_id, inbound_remark) so the file is readable on its own.
	header := []string{
		"inbound_id", "inbound_remark", "name", "psk", "enabled", "quota_bytes",
		"expires_at", "ip_limit", "reset_policy", "reset_day", "note",
	}
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	for _, c := range clients {
		record := []string{
			strconv.FormatInt(c.InboundID, 10),
			remarks[c.InboundID],
			c.Name,
			c.PSK,
			strconv.FormatBool(c.Enabled),
			strconv.FormatInt(c.QuotaBytes, 10),
			strconv.FormatInt(c.ExpiresAt, 10),
			strconv.Itoa(c.IPLimit),
			c.ResetPolicy,
			strconv.Itoa(c.ResetDay),
			c.Note,
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ExportFilename is the suggested download name for an export.
func ExportFilename(now time.Time) string {
	return "sspanel-clients-" + now.Format("20060102-150405") + ".csv"
}
