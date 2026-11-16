package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginapi "github.com/kryspal/plugin-sdk"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
)

const (
	// calendarRequests is how many of the member's latest requests the calendar looks at: one call to Seerr.
	calendarRequests = 100
	// calendarTitles is how many titles of those get a detail lookup (cached for a day), newest request first.
	calendarTitles = 40
	// calendarBudget is how long a call waits for the lookups; the host's limit is 15 seconds with the queue.
	calendarBudget = 9 * time.Second
	// seasonTTL is how long the episodes of a season are kept.
	seasonTTL = 6 * time.Hour
	seasonMax = 500
)

// TMDB release types of a movie (Seerr passes them on).
const (
	releaseTheatricalLimited = 2
	releaseTheatrical        = 3
	releaseDigital           = 4
	releasePhysical          = 5
)

// ListMemberCalendar lists the dates in the window that matter to the member: when a movie they requested reaches cinemas, streaming or disc,
// and the episodes of a series they requested that air. Seerr has no calendar of its own, so this is built from what Seerr already has: the
// member's requests (one call), the details of each title (cached for a day, shared with "My requests"), and the episodes of a season only
// when the next episode of its series falls in the window. A movie already available is left out; so is a request that was declined or failed.
func (p *plugin) ListMemberCalendar(ctx context.Context, req *pluginv1.ListMemberCalendarRequest) (*pluginv1.ListMemberCalendarResponse, error) {
	from, err := time.Parse("2006-01-02", req.GetFrom())
	if err != nil {
		return nil, sdk.InvalidArgument("bad from")
	}
	to, err := time.Parse("2006-01-02", req.GetTo())
	if err != nil || to.Before(from) || to.Sub(from) > (pluginapi.MaxCalendarDays-1)*24*time.Hour {
		return nil, sdk.InvalidArgument("bad to")
	}
	tg, err := p.api.target()
	if err != nil {
		return nil, err
	}
	uid, err := p.findUser(ctx, tg, req.GetMember())
	if err != nil {
		return nil, err
	}
	resp := &pluginv1.ListMemberCalendarResponse{}
	if uid == 0 {
		return resp, nil
	}
	rqs, err := p.calendarRequests(ctx, tg, uid, req.GetMember())
	if err != nil {
		return nil, err
	}
	tag := req.GetLanguages().GetLanguage()
	lang := tmdbLanguage(tag)
	region := regionOf(lang)
	t := messages.For(tag)

	// One lookup per title, newest request first.
	type title struct {
		kind string
		id   int
	}
	var titles []title
	seen := map[title]bool{}
	for _, rq := range rqs {
		kind, id, ok := requestTitle(rq)
		k := title{kind, id}
		if !ok || seen[k] || rq.Status == requestDeclined || rq.Status == requestFailed {
			continue
		}
		if kind == kindMovie && rq.Media.tierStatus(rq.Is4K) == mediaAvailable {
			continue
		}
		if len(titles) >= calendarTitles {
			break
		}
		seen[k] = true
		titles = append(titles, k)
	}
	budget, cancel := context.WithTimeout(ctx, calendarBudget)
	defer cancel()
	var waits []<-chan struct{}
	for _, k := range titles {
		if done := p.titles.start(p, tg, k.kind, k.id, lang); done != nil {
			waits = append(waits, done)
		}
	}
	awaitLookups(budget, waits, calendarBudget)

	fromS, toS := req.GetFrom(), req.GetTo()
	inWindow := func(date string) bool { return date >= fromS && date <= toS }
	note := truncate(t.T("calRequested"), pluginapi.MaxCalendarNote)

	// The series whose episodes are asked for run in parallel, a few at a time.
	var mu sync.Mutex
	var wg sync.WaitGroup
	add := func(es ...*pluginv1.CalendarEntry) {
		mu.Lock()
		resp.Entries = append(resp.Entries, es...)
		mu.Unlock()
	}
	for _, k := range titles {
		d, ok := p.titles.get(k.kind, k.id, lang)
		if !ok {
			continue // not answered in time: it is there on the next call
		}
		w := toWork(d, k.kind)
		if w == nil {
			continue
		}
		if k.kind == kindMovie {
			add(movieEntries(d, w, region, note, inWindow)...)
			continue
		}
		next := d.NextEpisodeToAir
		if next == nil || !validDate(next.AirDate) || next.AirDate > toS {
			continue
		}
		wg.Add(1)
		go func(id int, w *pluginv1.Work, next episode) {
			defer wg.Done()
			add(p.seriesEntries(budget, tg, id, lang, w, next, note, inWindow)...)
		}(k.id, w, *next)
	}
	wg.Wait()

	sort.SliceStable(resp.Entries, func(i, j int) bool {
		a, b := resp.Entries[i], resp.Entries[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.Work.Key != b.Work.Key {
			return a.Work.Key < b.Work.Key
		}
		return a.Episode < b.Episode
	})
	if len(resp.Entries) > pluginapi.MaxCalendarEntries {
		resp.Entries = resp.Entries[:pluginapi.MaxCalendarEntries]
	}
	return resp, nil
}

// calendarRequests is the member's latest requests, as ListMemberWorks asks for them (including a member whose Seerr account is gone).
func (p *plugin) calendarRequests(ctx context.Context, tg target, uid int, m *pluginv1.WorkMember) ([]request, error) {
	q := url.Values{"take": {strconv.Itoa(calendarRequests)}, "skip": {"0"}, "sort": {"modified"}, "sortDirection": {"desc"}}
	r, err := p.api.fetch(ctx, tg, uid, "/request", q)
	if err != nil {
		return nil, err
	}
	if r.Status == http.StatusForbidden {
		if uid, err = p.recheckUser(ctx, tg, m); err != nil {
			return nil, err
		}
		if uid == 0 {
			return nil, nil
		}
		if r, err = p.api.fetch(ctx, tg, uid, "/request", q); err != nil {
			return nil, err
		}
		if r.Status == http.StatusForbidden {
			slog.Warn("seerr refused to list a member's requests", "member", m.GetId())
			return nil, nil
		}
	}
	var list struct {
		Results []request `json:"results"`
	}
	if err := decode(r, &list); err != nil {
		return nil, err
	}
	return list.Results, nil
}

// movieEntries is the cinema, streaming and disc dates of a movie that fall in the window. For each of the three the date is the one of the
// member's region (the region of the language, else the United States), and when Seerr has none there the earliest anywhere.
func movieEntries(d result, w *pluginv1.Work, region, note string, inWindow func(string) bool) []*pluginv1.CalendarEntry {
	kinds := map[int]pluginv1.CalendarEntryKind{
		releaseTheatricalLimited: pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_THEATRICAL,
		releaseTheatrical:        pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_THEATRICAL,
		releaseDigital:           pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_DIGITAL,
		releasePhysical:          pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_PHYSICAL,
	}
	type pick struct{ own, us, any string }
	best := map[pluginv1.CalendarEntryKind]*pick{}
	earlier := func(cur, d string) string {
		if cur == "" || d < cur {
			return d
		}
		return cur
	}
	for _, c := range d.Releases.Results {
		for _, rd := range c.Dates {
			kind, ok := kinds[rd.Type]
			date := datePart(rd.ReleaseDate)
			if !ok || !validDate(date) {
				continue
			}
			p := best[kind]
			if p == nil {
				p = &pick{}
				best[kind] = p
			}
			p.any = earlier(p.any, date)
			if c.Country == region {
				p.own = earlier(p.own, date)
			}
			if c.Country == "US" {
				p.us = earlier(p.us, date)
			}
		}
	}
	var out []*pluginv1.CalendarEntry
	for _, kind := range []pluginv1.CalendarEntryKind{pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_THEATRICAL,
		pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_DIGITAL, pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_PHYSICAL} {
		p := best[kind]
		if p == nil {
			continue
		}
		date := p.own
		if date == "" {
			date = p.us
		}
		if date == "" {
			date = p.any
		}
		if inWindow(date) {
			out = append(out, &pluginv1.CalendarEntry{Work: w, Date: date, Kind: kind, Note: note})
		}
	}
	return out
}

// seriesEntries is the episodes of a series in the window. The details give the next episode; the season it belongs to gives the ones after
// it (and the next episode itself when the window is later than it). One lookup of one season, kept for six hours.
func (p *plugin) seriesEntries(ctx context.Context, tg target, id int, lang string, w *pluginv1.Work, next episode, note string, inWindow func(string) bool) []*pluginv1.CalendarEntry {
	eps := p.seasons.get(ctx, p, tg, id, next.SeasonNumber, lang)
	if len(eps) == 0 { // the season could not be read: the one episode the details gave
		eps = []episode{next}
	}
	var out []*pluginv1.CalendarEntry
	for _, e := range eps {
		e.SeasonNumber = next.SeasonNumber // the episodes of a season may not repeat its number
		if !validDate(e.AirDate) || !inWindow(e.AirDate) || e.AirDate < next.AirDate {
			continue
		}
		kind := pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_EPISODE
		if e.EpisodeNumber == 1 {
			kind = pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_SEASON_PREMIERE
			if e.SeasonNumber == 1 {
				kind = pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_PREMIERE
			}
		}
		out = append(out, &pluginv1.CalendarEntry{
			Work: w, Date: e.AirDate, Kind: kind, Season: int32(e.SeasonNumber), Episode: int32(e.EpisodeNumber),
			EpisodeTitle: truncate(strings.TrimSpace(e.Name), pluginapi.MaxCalendarEpisodeTitle), Note: note,
		})
	}
	return out
}

// regionOf is the country of a language tag in Seerr's form (zh-TW is TW; en has none and is the United States).
func regionOf(lang string) string {
	if _, r, ok := strings.Cut(lang, "-"); ok && len(r) == 2 {
		return strings.ToUpper(r)
	}
	return "US"
}

// datePart is the date of an ISO time ("1999-03-24T00:00:00.000Z"): TMDB gives release dates with a placeholder time of day.
func datePart(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func validDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil && len(s) == 10
}

// seasonCache keeps the episodes of a season for six hours, at most seasonMax seasons. The lookups share the slots of the title lookups.
type seasonCache struct {
	mu  sync.Mutex
	m   map[string]seasonEntry
	now func() time.Time
}

type seasonEntry struct {
	at  time.Time
	eps []episode
}

func newSeasonCache() *seasonCache { return &seasonCache{m: map[string]seasonEntry{}, now: time.Now} }

func (c *seasonCache) get(ctx context.Context, p *plugin, tg target, id, season int, lang string) []episode {
	key := strconv.Itoa(id) + ":" + strconv.Itoa(season) + ":" + lang
	c.mu.Lock()
	e, ok := c.m[key]
	c.mu.Unlock()
	if ok && c.now().Sub(e.at) <= seasonTTL {
		return e.eps
	}
	select {
	case p.titles.slots <- struct{}{}:
		defer func() { <-p.titles.slots }()
	case <-ctx.Done():
		return nil
	}
	var out struct {
		Episodes []episode `json:"episodes"`
	}
	if err := p.api.get(ctx, tg, "/tv/"+strconv.Itoa(id)+"/season/"+strconv.Itoa(season), url.Values{"language": {lang}}, &out); err != nil {
		return nil
	}
	c.mu.Lock()
	if len(c.m) >= seasonMax {
		c.m = map[string]seasonEntry{}
	}
	c.m[key] = seasonEntry{at: c.now(), eps: out.Episodes}
	c.mu.Unlock()
	return out.Episodes
}

func (c *seasonCache) clear() {
	c.mu.Lock()
	c.m = map[string]seasonEntry{}
	c.mu.Unlock()
}
