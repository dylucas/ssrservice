package service

import (
	"time"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/store"
)

// Access analytics retention. Raw records are browsing history and are kept for
// one week; the per-day rollup keeps a quarter so the panel can still describe
// a trend after the individual visits are gone.
const (
	AccessRawRetentionDays       = 7
	AccessAggregateRetentionDays = 90
	// accessPruneInterval is how many minutes pass between prune runs on the
	// existing one minute scheduler tick. Retention has to keep up with the
	// intake, and one TCP connection or UDP destination window per record means a
	// busy instance produces rows continuously: at one run an hour the allowance
	// was tens of records per second, far below a real workload. A run with
	// nothing to delete is a single indexed probe, so this can be every tick.
	accessPruneInterval = 1
	// accessFoldBackstopTicks is how often every recent day is re-checked for
	// destination-cardinality overflow, not just the current one. A fold that is
	// missed while the clock crosses midnight (or while the process is stopped)
	// is repaired on the next hourly pass instead of never.
	accessFoldBackstopTicks = 60
	// accessFoldWindowDays is how many days the backstop pass covers.
	accessFoldWindowDays = 7
	// AccessMaxWindowDays bounds the summary window. The rollup keeps 90 days,
	// so a longer window would only mean an empty prefix.
	AccessMaxWindowDays = AccessAggregateRetentionDays
)

// AccessEnabled reports whether the data plane is recording access records.
func (s *Service) AccessEnabled() bool { return s.mgr.AccessEnabled() }

// AccessDropped returns how many records the runtime dropped since start.
func (s *Service) AccessDropped() uint64 { return s.mgr.AccessDropped() }

// AccessEvents returns one page of raw access records.
//
// A window given as local day boundaries is resolved here, in the effective time
// zone, so "the day the operator picked" means the same day the summary buckets
// its rows by even when the browser's zone differs from the server's. Unix
// second bounds (Since/Until) are taken as absolute instants.
func (s *Service) AccessEvents(filter store.AccessEventFilter) ([]model.AccessEvent, int64, error) {
	if filter.SinceDay != "" || filter.UntilDay != "" {
		loc := s.location()
		since, until, err := dayWindow(filter.SinceDay, filter.UntilDay, loc)
		if err != nil {
			return nil, 0, err
		}
		filter.Since, filter.Until = since, until
	}
	return s.store.ListAccessEvents(filter)
}

// dayWindow turns inclusive local day boundaries into the half-open unix second
// window the store filters on. An empty bound is open on that side; either may be
// given, but the pair has to be ordered.
func dayWindow(sinceDay, untilDay string, loc *time.Location) (int64, int64, error) {
	var since, until int64
	if sinceDay != "" {
		start, err := time.ParseInLocation("2006-01-02", sinceDay, loc)
		if err != nil {
			return 0, 0, invalid("since_day must be a date in YYYY-MM-DD form")
		}
		since = start.Unix()
	}
	if untilDay != "" {
		start, err := time.ParseInLocation("2006-01-02", untilDay, loc)
		if err != nil {
			return 0, 0, invalid("until_day must be a date in YYYY-MM-DD form")
		}
		// The picker's end date is inclusive, and the store's upper bound is
		// exclusive: ask for the start of the following day. AddDate keeps this
		// correct across a daylight-saving change, where adding 24 hours would
		// be an hour off.
		until = start.AddDate(0, 0, 1).Unix()
	}
	if since > 0 && until > 0 && until <= since {
		return 0, 0, invalid("until_day must not be before since_day")
	}
	return since, until, nil
}

// AccessSummary builds the overview of the last `days` days.
//
// The window is evaluated in the effective time zone, like every other dated
// view in the panel: after a settings change the config file is stale and the
// day buckets are not.
func (s *Service) AccessSummary(days int, clientID, inboundID int64) (model.AccessSummary, error) {
	if days <= 0 {
		days = AccessRawRetentionDays
	}
	if days > AccessMaxWindowDays {
		days = AccessMaxWindowDays
	}
	loc := s.location()
	now := time.Now().In(loc)
	first := now.AddDate(0, 0, -(days - 1))
	summary, err := s.store.AccessSummary(store.AccessSummaryFilter{
		ClientID:  clientID,
		InboundID: inboundID,
		SinceDay:  first.Format("2006-01-02"),
		TopLimit:  10,
	})
	if err != nil {
		return model.AccessSummary{}, err
	}
	// The rollup only has rows for days that saw traffic. The chart is plotted by
	// index, so a missing day would compress the gap and label the axis with
	// dates that are days apart while the points sit next to each other.
	summary.Daily = fillAccessDays(summary.Daily, first, days)
	return summary, nil
}

// fillAccessDays expands the rollup rows to one row per day of the window,
// inserting zeros for the days without a record.
func fillAccessDays(daily []model.AccessDayTotal, first time.Time, days int) []model.AccessDayTotal {
	byDay := make(map[string]model.AccessDayTotal, len(daily))
	for _, row := range daily {
		byDay[row.Day] = row
	}
	filled := make([]model.AccessDayTotal, 0, days)
	for offset := 0; offset < days; offset++ {
		day := first.AddDate(0, 0, offset).Format("2006-01-02")
		if row, ok := byDay[day]; ok {
			filled = append(filled, row)
			continue
		}
		filled = append(filled, model.AccessDayTotal{Day: day})
	}
	return filled
}

// AccessRetention exposes the retention policy to the panel so the overview can
// describe it without duplicating the numbers.
func AccessRetention() (rawDays, aggregateDays int) {
	return AccessRawRetentionDays, AccessAggregateRetentionDays
}

// pruneAccess drops raw events past the one week window, rollup rows past the
// quarterly window, and folds the destination overflow of the recent days. A
// failure is only logged: the next tick retries.
//
// Both cutoffs are derived in the effective time zone. The raw cutoff is an
// absolute instant, but "seven days back" means midnight-aligned days like every
// other dated view in the panel, so deriving it from the local clock would let
// the newest day be trimmed early or late after a time zone change.
//
// backstop widens the fold to every recent day instead of just today; it runs
// hourly, so the steady cost stays one day of rollup rows per minute.
func (s *Service) pruneAccess(backstop bool) {
	now := time.Now().In(s.location())
	rawCutoff := now.AddDate(0, 0, -AccessRawRetentionDays).Unix()
	dayCutoff := now.AddDate(0, 0, -AccessAggregateRetentionDays).Format("2006-01-02")
	events, daily, err := s.store.PruneAccess(rawCutoff, dayCutoff)
	if err != nil {
		s.logger.Warn("prune access records failed", "error", err)
		return
	}
	if events > 0 || daily > 0 {
		s.logger.Info("pruned access records", "events", events, "daily_rows", daily,
			"raw_days", AccessRawRetentionDays, "aggregate_days", AccessAggregateRetentionDays)
	}

	// Fold after pruning: the cutoff may have just removed whole days, and the
	// days that still grow are the ones worth checking.
	window := 0
	if backstop {
		window = accessFoldWindowDays - 1
	}
	since := now.AddDate(0, 0, -window).Format("2006-01-02")
	folded, err := s.store.FoldAccessHosts(since, model.AccessMaxHostsPerKey, 0)
	if err != nil {
		s.logger.Warn("fold access destinations failed", "error", err)
		return
	}
	if folded > 0 {
		s.logger.Info("folded access destinations", "rows", folded,
			"cap", model.AccessMaxHostsPerKey, "since", since)
	}
}
