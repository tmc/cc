package service

import (
	"context"
	"time"

	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/ccusage"
)

// UsageReport is everything a usage view draws from: the rows the store
// returned, converted to the report package's own types, and the derived
// series and summary. The CLI and the web API both build one, so that a
// number shown in a terminal and the same number shown in a browser come
// from one implementation.
type UsageReport struct {
	Window  ccusage.Window        `json:"-"`
	Hours   []ccusage.Hour        `json:"hours,omitempty"`
	Groups  []ccusage.Group       `json:"groups,omitempty"`
	Spans   []ccusage.SessionSpan `json:"-"`
	Days    []ccusage.Day         `json:"days,omitempty"`
	Summary ccusage.Summary       `json:"summary"`
}

// UsageReportOptions selects what a report covers. A zero Window means the
// whole index, in which case the window is fitted to the rows that come back.
type UsageReportOptions struct {
	Filter store.UsageFilter
	Window ccusage.Window
	Unit   string
	Price  ccusage.Pricing
}

// UsageReport runs the three usage queries a view needs and derives the daily
// series and the summary from them.
func (s *Service) UsageReport(ctx context.Context, opts UsageReportOptions) (*UsageReport, error) {
	hours, err := s.store.UsageByHour(ctx, opts.Filter)
	if err != nil {
		return nil, err
	}
	groups, err := s.store.UsageByGroup(ctx, opts.Filter)
	if err != nil {
		return nil, err
	}
	spans, err := s.store.UsageSessions(ctx, opts.Filter)
	if err != nil {
		return nil, err
	}
	rep := &UsageReport{
		Window: opts.Window,
		Hours:  UsageHours(hours),
		Groups: UsageGroups(groups),
		Spans:  UsageSpans(spans),
	}
	if rep.Window.Start.IsZero() {
		// The whole index: fit the window to the rows that came back, as the
		// command line does when -since is 0.
		loc := opts.Window.Loc
		if loc == nil {
			loc = time.Local
		}
		rep.Window = ccusage.WindowEnding(time.Now(), 0, loc, rep.Hours)
	}
	rep.Days = ccusage.Days(rep.Hours, rep.Window)
	rep.Summary = ccusage.Summarize(rep.Days, rep.Groups, rep.Spans, rep.Window, opts.Unit, opts.Price)
	return rep, nil
}

// UsageHours converts store rows to the report package's own types, which is
// what keeps ccusage free of a dependency on the store.
func UsageHours(rows []store.HourRow) []ccusage.Hour {
	out := make([]ccusage.Hour, len(rows))
	for i, r := range rows {
		out[i] = ccusage.Hour{
			Hour: r.Hour, Requests: r.Requests, Estimated: r.Estimated,
			Tokens: ccusage.Tokens{Input: r.Input, CacheRead: r.CacheRead,
				CacheCreate: r.CacheCreate, Output: r.Output},
		}
	}
	return out
}

// UsageGroups converts store group rows to report groups.
func UsageGroups(rows []store.GroupRow) []ccusage.Group {
	out := make([]ccusage.Group, len(rows))
	for i, r := range rows {
		out[i] = ccusage.Group{
			Repo: r.Repo, Agent: r.Agent, Vendor: r.Vendor, Model: r.Model,
			Sessions: r.Sessions, Requests: r.Requests, First: r.First, Last: r.Last,
			Estimated: r.Estimated,
			Tokens: ccusage.Tokens{Input: r.Input, CacheRead: r.CacheRead,
				CacheCreate: r.CacheCreate, Output: r.Output},
		}
	}
	return out
}

// UsageSpans converts store session rows to report spans.
func UsageSpans(rows []store.SessionSpanRow) []ccusage.SessionSpan {
	out := make([]ccusage.SessionSpan, len(rows))
	for i, r := range rows {
		out[i] = ccusage.SessionSpan{SessionID: r.SessionID, First: r.First, Last: r.Last, Requests: r.Requests}
	}
	return out
}
