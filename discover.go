package main

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"unicode/utf8"

	pluginapi "github.com/kryspal/plugin-sdk"
	"github.com/kryspal/plugin-sdk/i18n"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
)

// A work's key is "movie:<TMDB number>" or "tv:<TMDB number>". It holds nothing secret.
const (
	kindMovie = "movie"
	kindTV    = "tv"
)

func workKey(kind string, id int) string { return kind + ":" + strconv.Itoa(id) }

// parseKey reads a key made by workKey; ok is false for anything else.
func parseKey(key string) (kind string, id int, ok bool) {
	k, n, found := strings.Cut(key, ":")
	if !found || (k != kindMovie && k != kindTV) {
		return "", 0, false
	}
	id, err := strconv.Atoi(n)
	if err != nil || id <= 0 || strconv.Itoa(id) != n {
		return "", 0, false
	}
	return k, id, true
}

func kindOf(t pluginv1.ItemType) string {
	switch t {
	case pluginv1.ItemType_ITEM_TYPE_MOVIE:
		return kindMovie
	case pluginv1.ItemType_ITEM_TYPE_SERIES:
		return kindTV
	}
	return ""
}

func itemType(kind string) pluginv1.ItemType {
	if kind == kindTV {
		return pluginv1.ItemType_ITEM_TYPE_SERIES
	}
	return pluginv1.ItemType_ITEM_TYPE_MOVIE
}

// A row is one list of the discover page. path is the Seerr endpoint; trending also takes the media type.
type rowDef struct {
	id       string
	titleKey string
	path     string
	kind     string // movie or tv: the row only exists for that type; empty: both
	trending bool
	// userLocale: Seerr uses the query's language here as an original-language filter and titles in the Seerr user's own locale (v3.5.1,
	// /discover/movies and /discover/tv), so the language is not sent and the titles are localized afterwards.
	userLocale bool
	// filterable: the row honors the genre, period and sort choices (its filter_keys).
	filterable bool
}

var rowDefs = []rowDef{
	{id: "trending", titleKey: "rowTrending", path: "/discover/trending", trending: true},
	{id: "popular_movies", titleKey: "rowPopularMovies", path: "/discover/movies", kind: kindMovie, userLocale: true, filterable: true},
	{id: "popular_series", titleKey: "rowPopularSeries", path: "/discover/tv", kind: kindTV, userLocale: true, filterable: true},
}

func (p *plugin) rowsFor(kind string) []rowDef {
	var out []rowDef
	for _, r := range rowDefs {
		if kind == "" || r.kind == "" || r.kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// filterKeys are the filter and sort keys the row honors; none for a row that cannot be filtered.
func (d rowDef) filterKeys() []string {
	if !d.filterable {
		return nil
	}
	return []string{keyGenre, keyPeriod, keyLang, keyRating, keySort}
}

func language(l *pluginv1.Languages) string { return tmdbLanguage(l.GetLanguage()) }

// ListDiscoverRows returns every row with its first page, or one row from a page token. A row that fails while others work is left out.
func (p *plugin) ListDiscoverRows(ctx context.Context, req *pluginv1.ListDiscoverRowsRequest) (*pluginv1.ListDiscoverRowsResponse, error) {
	tg, err := p.api.target()
	if err != nil {
		return nil, err
	}
	t := messages.For(req.GetLanguages().GetLanguage())
	kind := kindOf(req.GetType())
	defs := p.rowsFor(kind)
	pageNo := 1
	if id := req.GetRowId(); id != "" {
		var one []rowDef
		for _, d := range defs {
			if d.id == id {
				one = append(one, d)
			}
		}
		if len(one) == 0 {
			return nil, sdk.NotFound("unknown row")
		}
		defs = one
		if pageNo, err = parsePage(req.GetPageToken()); err != nil {
			return nil, err
		}
	}
	// Seerr's locale is looked up while the lists are fetched, not after.
	locCh := make(chan string, 1)
	go func() { locCh <- p.seerrLocale(ctx, tg) }()
	var locOnce sync.Once
	var locVal string
	loc := func() string {
		locOnce.Do(func() { locVal = <-locCh })
		return locVal
	}
	rows := make([]*pluginv1.DiscoverRow, len(defs))
	errs := make([]error, len(defs))
	var wg sync.WaitGroup
	for i, d := range defs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lang := language(req.GetLanguages())
			q := url.Values{"page": {strconv.Itoa(pageNo)}}
			if !d.userLocale {
				q.Set("language", lang)
			}
			title := t.T(d.titleKey)
			if d.filterable {
				sortValue := applyFilters(q, d.kind, selected(req.GetSelections()), time.Now().UTC().Format("2006-01-02"))
				if sortValue != defaultSort {
					title = t.T(map[string]string{"newest": "rowNewest", "top_rated": "rowTop"}[sortValue] + map[string]string{kindMovie: "Movies", kindTV: "Series"}[d.kind])
				}
			}
			if d.trending {
				q.Set("mediaType", map[string]string{"": "all", kindMovie: kindMovie, kindTV: kindTV}[kind])
			}
			pg, err := p.page(ctx, tg, d.path, q)
			if err != nil {
				errs[i] = err
				return
			}
			if d.userLocale {
				pg.Results = p.localize(ctx, tg, pg.Results, lang, loc())
			}
			keys := d.filterKeys()
			if d.filterable && len(p.genres.list(ctx, p, tg, d.kind, req.GetLanguages().GetLanguage())) == 0 {
				keys = []string{keyPeriod, keyLang, keyRating, keySort} // no genre list from Seerr: the filter is not declared either
			}
			rows[i] = &pluginv1.DiscoverRow{Id: d.id, Title: title, FilterKeys: keys, Works: works(pg.Results, first(d.kind, kind)), NextPageToken: nextToken(pg)}
		}()
	}
	wg.Wait()
	resp := &pluginv1.ListDiscoverRowsResponse{}
	var firstErr error
	for i, r := range rows {
		switch {
		case errs[i] != nil:
			slog.Warn("discover row failed", "row", defs[i].id, "error", errs[i].Error())
			if firstErr == nil {
				firstErr = errs[i]
			}
		case len(r.Works) > 0:
			resp.Rows = append(resp.Rows, r)
		default:
			slog.Warn("discover row is empty", "row", defs[i].id, "page", pageNo)
		}
	}
	if len(resp.Rows) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return resp, nil
}

// SearchWorks searches Seerr. It cannot filter by type, so a page may hold fewer works than Seerr's page size.
func (p *plugin) SearchWorks(ctx context.Context, req *pluginv1.SearchWorksRequest) (*pluginv1.SearchWorksResponse, error) {
	tg, err := p.api.target()
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(req.GetQuery())
	if query == "" {
		return &pluginv1.SearchWorksResponse{}, nil
	}
	pageNo, err := parsePage(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	q := url.Values{"query": {query}, "page": {strconv.Itoa(pageNo)}, "language": {language(req.GetLanguages())}}
	pg, err := p.page(ctx, tg, "/search", q)
	if err != nil {
		return nil, err
	}
	return &pluginv1.SearchWorksResponse{Works: works(pg.Results, kindOf(req.GetType())), NextPageToken: nextToken(pg)}, nil
}

// GetWork returns one title in full: the details, the external IDs and, for a series, its seasons.
func (p *plugin) GetWork(ctx context.Context, req *pluginv1.GetWorkRequest) (*pluginv1.Work, error) {
	tg, err := p.api.target()
	if err != nil {
		return nil, err
	}
	kind, id, ok := parseKey(req.GetKey())
	if !ok {
		return nil, sdk.NotFound("unknown work")
	}
	var r result
	if err := p.api.get(ctx, tg, "/"+kind+"/"+strconv.Itoa(id), url.Values{"language": {language(req.GetLanguages())}}, &r); err != nil {
		return nil, err
	}
	r.ID, r.MediaType = id, kind
	w := toWork(r, kind)
	if w == nil {
		return nil, sdk.NotFound("unknown work")
	}
	w.RuntimeMinutes = int32(r.Runtime)
	if kind == kindTV && len(r.EpisodeRun) > 0 {
		w.RuntimeMinutes = int32(r.EpisodeRun[0])
	}
	if r.ExternalIDs.IMDb != "" {
		w.ProviderIds["imdb"] = r.ExternalIDs.IMDb
	}
	if r.ExternalIDs.TVDB > 0 && kind == kindTV {
		w.ProviderIds["tvdb"] = strconv.Itoa(r.ExternalIDs.TVDB)
	}
	w.ContentRating = contentRating(r, kind)
	if kind == kindTV {
		t := messages.For(req.GetLanguages().GetLanguage())
		for _, s := range r.Seasons {
			if len(w.Seasons) >= pluginapi.MaxWorkSeasons {
				break
			}
			w.Seasons = append(w.Seasons, &pluginv1.WorkSeason{Number: int32(s.SeasonNumber), Name: seasonName(t, s.SeasonNumber, s.Name),
				EpisodeCount: int32(s.EpisodeCount), Year: year(s.AirDate)})
		}
	}
	return w, nil
}

// contentRating picks the United States certification, the one Seerr always has, else the first one.
func contentRating(r result, kind string) string {
	if kind == kindTV {
		first := ""
		for _, c := range r.ContentRatings.Results {
			if c.Rating == "" {
				continue
			}
			if c.Country == "US" {
				return c.Rating
			}
			if first == "" {
				first = c.Rating
			}
		}
		return first
	}
	first := ""
	for _, c := range r.Releases.Results {
		for _, d := range c.Dates {
			if d.Certification == "" {
				continue
			}
			if c.Country == "US" {
				return d.Certification
			}
			if first == "" {
				first = d.Certification
			}
		}
	}
	return first
}

// seasonName is Seerr's name for the season (in the request's language), else a generic one.
func seasonName(t i18n.Translator, number int, seerrName string) string {
	if n := truncate(strings.TrimSpace(seerrName), pluginapi.MaxWorkSeasonName); n != "" {
		return n
	}
	return genericSeason(t, number)
}

func genericSeason(t i18n.Translator, number int) string {
	if number == 0 {
		return t.T("specials")
	}
	return t.Tf("season", number)
}

// truncate cuts s to at most n characters.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// works converts the movies and series of a list; people and entries without a usable title are dropped. only limits the list to one kind
// (empty keeps both).
func works(rs []result, only string) []*pluginv1.Work {
	out := make([]*pluginv1.Work, 0, len(rs))
	for _, r := range rs {
		kind := r.MediaType
		if kind != kindMovie && kind != kindTV || (only != "" && kind != only) {
			continue
		}
		if w := toWork(r, kind); w != nil {
			out = append(out, w)
		}
		if len(out) >= pluginapi.MaxWorksPerPage {
			break
		}
	}
	return out
}

func toWork(r result, kind string) *pluginv1.Work {
	title, orig, date := r.Title, r.OriginalTitle, r.ReleaseDate
	if kind == kindTV {
		title, orig, date = r.Name, r.OriginalName, r.FirstAirDate
	}
	title = strings.TrimSpace(title)
	if r.ID <= 0 || title == "" {
		return nil
	}
	w := &pluginv1.Work{
		Key: workKey(kind, r.ID), Type: itemType(kind), Title: title, OriginalTitle: strings.TrimSpace(orig), Year: year(date),
		Overview: r.Overview, ProviderIds: map[string]string{providerTMDB: strconv.Itoa(r.ID)},
	}
	if r.PosterPath != "" {
		w.PosterUrl = imageBase + posterSize + r.PosterPath
	}
	if r.BackdropPath != "" {
		w.BackdropUrl = imageBase + backdropSize + r.BackdropPath
	}
	return w
}

func parsePage(token string) (int, error) {
	if token == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(token)
	if err != nil || n < 1 || n > 1000 {
		return 0, sdk.InvalidArgument("bad page token")
	}
	return n, nil
}

func nextToken(pg page) string {
	if pg.Page >= 1 && pg.Page < pg.TotalPages {
		return strconv.Itoa(pg.Page + 1)
	}
	return ""
}

// localize gives the titles of a list the member's language. Seerr v3.5.1 titles /discover/movies and /discover/tv in the locale of the Seerr
// user (the member, through X-API-User, else the server default), and takes no display language there. When that locale is the language wanted
// the list is as it is. Otherwise each title is looked up in the language: answers are kept (per title and language), the lookups run a few at a
// time, and the call waits at most localizeBudget for them. Titles not found by then keep Seerr's text this time and are filled in by lookups
// that go on in the background, so the next call has them.
func (p *plugin) localize(ctx context.Context, tg target, rs []result, lang, seerrLoc string) []result {
	if len(rs) == 0 || sameLanguage(seerrLoc, lang) {
		return rs
	}
	var waits []<-chan struct{}
	for _, r := range rs {
		if (r.MediaType == kindMovie || r.MediaType == kindTV) && strings.TrimSpace(r.Title+r.Name) != "" {
			if done := p.titles.start(p, tg, r.MediaType, r.ID, lang); done != nil {
				waits = append(waits, done)
			}
		}
	}
	awaitLookups(ctx, waits, p.titles.budget)
	out := make([]result, len(rs))
	copy(out, rs)
	for i := range out {
		if l, ok := p.titles.get(out[i].MediaType, out[i].ID, lang); ok {
			out[i].Title, out[i].Name = l.Title, l.Name
			if l.Overview != "" {
				out[i].Overview = l.Overview
			}
			if l.PosterPath != "" {
				out[i].PosterPath = l.PosterPath
			}
		}
	}
	return out
}

// seerrLocale is the locale Seerr titles /discover/movies and /discover/tv in for the plugin's own key: the locale in the settings of the
// key's user when it has one, else Seerr's default locale (Settings, General; v3.5.1 reads them in that order). It is written the way
// the member's language is (tmdbLanguage), kept for five minutes, and "" when it cannot be read, which makes every list be looked up in the
// language wanted.
func (p *plugin) seerrLocale(ctx context.Context, tg target) string {
	if l, ok := p.locale.get(); ok {
		return l
	}
	var me struct {
		ID int `json:"id"`
	}
	var own struct {
		Locale string `json:"locale"`
	}
	var def struct {
		Locale string `json:"locale"`
	}
	if err := p.api.get(ctx, tg, "/auth/me", nil, &me); err != nil || me.ID <= 0 {
		return ""
	}
	// The key's own setting wins when it has one; both are asked at once, a no-answer for the own setting only means it has none.
	var wg sync.WaitGroup
	var defErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = p.api.get(ctx, tg, "/user/"+strconv.Itoa(me.ID)+"/settings/main", nil, &own)
	}()
	go func() {
		defer wg.Done()
		defErr = p.api.get(ctx, tg, "/settings/main", nil, &def)
	}()
	wg.Wait()
	loc := strings.TrimSpace(own.Locale)
	if loc == "" {
		if defErr != nil {
			return ""
		}
		loc = strings.TrimSpace(def.Locale)
	}
	if loc == "" {
		return ""
	}
	loc = tmdbLanguage(loc)
	p.locale.put(loc)
	return loc
}

// sameLanguage is true when two language tags give the same titles for practical purposes: equal, or both English (the regions of English
// differ in few titles).
func sameLanguage(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = tmdbLanguage(a), tmdbLanguage(b)
	if strings.EqualFold(a, b) {
		return true
	}
	base := func(s string) string { return strings.ToLower(strings.SplitN(s, "-", 2)[0]) }
	return base(a) == "en" && base(b) == "en"
}

// awaitLookups waits for the lookups to end, at most budget, or until ctx ends.
func awaitLookups(ctx context.Context, waits []<-chan struct{}, budget time.Duration) {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for _, done := range waits {
		select {
		case <-done:
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}
