package main

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"sync"

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
}

var rowDefs = []rowDef{
	{id: "trending", titleKey: "rowTrending", path: "/discover/trending", trending: true},
	{id: "popular_movies", titleKey: "rowPopularMovies", path: "/discover/movies", kind: kindMovie},
	{id: "popular_series", titleKey: "rowPopularSeries", path: "/discover/tv", kind: kindTV},
	{id: "upcoming_movies", titleKey: "rowUpcomingMovies", path: "/discover/movies/upcoming", kind: kindMovie},
	{id: "upcoming_series", titleKey: "rowUpcomingSeries", path: "/discover/tv/upcoming", kind: kindTV},
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
	rows := make([]*pluginv1.DiscoverRow, len(defs))
	errs := make([]error, len(defs))
	var wg sync.WaitGroup
	for i, d := range defs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := url.Values{"page": {strconv.Itoa(pageNo)}, "language": {language(req.GetLanguages())}}
			if d.trending {
				q.Set("mediaType", map[string]string{"": "all", kindMovie: kindMovie, kindTV: kindTV}[kind])
			}
			var pg page
			if err := p.api.get(ctx, tg, d.path, q, &pg); err != nil {
				errs[i] = err
				return
			}
			rows[i] = &pluginv1.DiscoverRow{Id: d.id, Title: t.T(d.titleKey), Works: works(pg.Results, first(d.kind, kind)), NextPageToken: nextToken(pg)}
		}()
	}
	wg.Wait()
	resp := &pluginv1.ListDiscoverRowsResponse{}
	var firstErr error
	for i, r := range rows {
		switch {
		case errs[i] != nil:
			if firstErr == nil {
				firstErr = errs[i]
			}
		case len(r.Works) > 0:
			resp.Rows = append(resp.Rows, r)
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
	var pg page
	q := url.Values{"query": {query}, "page": {strconv.Itoa(pageNo)}, "language": {language(req.GetLanguages())}}
	if err := p.api.get(ctx, tg, "/search", q, &pg); err != nil {
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
