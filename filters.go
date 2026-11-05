package main

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
)

// The keys of the filters and the sort a kind declares. They are the same for movies and series.
const (
	keyGenre  = "genre"
	keyPeriod = "period"
	keyLang   = "language"
	keyRating = "rating"
	keySort   = "sort"
)

// A period is a range of release (movies) or first air (series) dates. The host has no range input, so each range is one choice.
type period struct {
	value    string
	from, to string // dates, YYYY-MM-DD; empty is open
	decade   int    // label: "<decade>s"; 0 for the open-ended first entry
}

var periods = []period{
	{value: "2020s", from: "2020-01-01", to: "2029-12-31", decade: 2020},
	{value: "2010s", from: "2010-01-01", to: "2019-12-31", decade: 2010},
	{value: "2000s", from: "2000-01-01", to: "2009-12-31", decade: 2000},
	{value: "1990s", from: "1990-01-01", to: "1999-12-31", decade: 1990},
	{value: "1980s", from: "1980-01-01", to: "1989-12-31", decade: 1980},
	{value: "older", to: "1979-12-31"},
}

// An original language: the value the member picks, the code Seerr's discover endpoints take (with_original_language), and the message key
// of its name. Chinese is zh and cn (TMDB files Cantonese-language titles under cn); "|" means or, and Seerr passes it on (tested on v3.5.1).
type origLang struct {
	value, code, label string
}

var origLangs = []origLang{
	{"zh", "zh|cn", "langZh"}, {"ja", "ja", "langJa"}, {"ko", "ko", "langKo"}, {"en", "en", "langEn"},
	{"fr", "fr", "langFr"}, {"de", "de", "langDe"}, {"es", "es", "langEs"},
}

// Minimum average votes the member can ask for. A title with a high average and a handful of votes is not good evidence, so the vote count has
// a floor of its own.
var ratings = []string{"6", "7", "8"}

var ratingMinVotes = map[string]int{kindMovie: 200, kindTV: 100}

// A sort is a value of the sort control: Seerr's sortBy, and a vote floor so a handful of votes cannot top a list.
type sortDef struct {
	value    string
	movieBy  string // sortBy for movies; empty is Seerr's default (popularity)
	seriesBy string
	minVotes map[string]int // by kind
	today    bool           // never list titles that are not out yet
	label    string         // message key of the control's choice
}

var sorts = []sortDef{
	{value: "popular", label: "sortPopular"},
	{value: "newest", movieBy: "primary_release_date.desc", seriesBy: "first_air_date.desc", today: true, label: "sortNewest"},
	{value: "top_rated", movieBy: "vote_average.desc", seriesBy: "vote_average.desc", minVotes: map[string]int{kindMovie: 500, kindTV: 200}, label: "sortTop"},
}

const defaultSort = "popular"

func findPeriod(v string) (period, bool) {
	for _, p := range periods {
		if p.value == v {
			return p, true
		}
	}
	return period{}, false
}

func findSort(v string) sortDef {
	for _, s := range sorts {
		if s.value == v {
			return s
		}
	}
	return sorts[0] // a value this plugin does not know counts as the default
}

// selected reads the member's choices into key → values.
func selected(sel []*pluginv1.WorkSelection) map[string][]string {
	out := map[string][]string{}
	for _, s := range sel {
		out[s.GetKey()] = s.GetValues()
	}
	return out
}

// applyFilters adds the member's choices to a /discover/movies or /discover/tv query. Values are checked again here: a genre must be a
// number, a period and a sort must be known; anything else is ignored. today is the date "newest" stops at.
func applyFilters(q url.Values, kind string, sel map[string][]string, today string) (sortValue string) {
	// Several genres mean "any of them": Seerr passes "|" to TMDB as or (a comma would be and).
	var genres []string
	seen := map[int]bool{}
	for _, g := range sel[keyGenre] {
		if n, err := strconv.Atoi(g); err == nil && n > 0 && !seen[n] {
			seen[n] = true
			genres = append(genres, strconv.Itoa(n))
		}
	}
	if len(genres) > 0 {
		q.Set("genre", strings.Join(genres, "|"))
	}
	// Seerr reads "language" here as the original language. Without a choice say "all", so a default original language set in Seerr does not
	// narrow the list.
	q.Set("language", "all")
	if v := sel[keyLang]; len(v) == 1 {
		for _, l := range origLangs {
			if l.value == v[0] {
				q.Set("language", l.code)
			}
		}
	}
	minVotes := 0
	if v := sel[keyRating]; len(v) == 1 {
		for _, r := range ratings {
			if r == v[0] {
				q.Set("voteAverageGte", r)
				minVotes = ratingMinVotes[kind]
			}
		}
	}
	gteKey, lteKey := "primaryReleaseDateGte", "primaryReleaseDateLte"
	if kind == kindTV {
		gteKey, lteKey = "firstAirDateGte", "firstAirDateLte"
	}
	var from, to string
	if v := sel[keyPeriod]; len(v) == 1 {
		if p, ok := findPeriod(v[0]); ok {
			from, to = p.from, p.to
		}
	}
	s := findSort(defaultSort)
	if v := sel[keySort]; len(v) == 1 {
		s = findSort(v[0])
	}
	if s.today && (to == "" || to > today) {
		to = today // "newest" must not start with titles that are not out yet
	}
	if from != "" {
		q.Set(gteKey, from)
	}
	if to != "" {
		q.Set(lteKey, to)
	}
	by := s.movieBy
	if kind == kindTV {
		by = s.seriesBy
	}
	if by != "" {
		q.Set("sortBy", by)
	}
	if n := s.minVotes[kind]; n > minVotes {
		minVotes = n
	}
	if minVotes > 0 {
		q.Set("voteCountGte", strconv.Itoa(minVotes))
	}
	return s.value
}

// ---- DescribeDiscoverSource ----

// DescribeDiscoverSource declares the two types and, for each, the genre and period filters and the sort. The genres come from Seerr and are
// kept for ten minutes; when Seerr cannot be asked, the last list is used, and without one the description fails (UNAVAILABLE), so the host asks again rather than keep a description without the genre filter.
func (p *plugin) DescribeDiscoverSource(ctx context.Context, req *pluginv1.DescribeDiscoverSourceRequest) (*pluginv1.DiscoverSourceInfo, error) {
	tg, err := p.api.target()
	if err != nil {
		return nil, err
	}
	tag := req.GetLanguages().GetLanguage()
	t := messages.For(tag)
	info := &pluginv1.DiscoverSourceInfo{}
	kinds := []string{kindMovie, kindTV}
	genres := make([][]genre, len(kinds))
	gerrs := make([]error, len(kinds))
	var wg sync.WaitGroup
	for i, kind := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			genres[i], gerrs[i] = p.genres.list(ctx, p, tg, kind, tag)
		}()
	}
	wg.Wait()
	for _, e := range gerrs {
		if e != nil {
			// A description without the genre filter would be kept by the host until the plugin is reloaded: fail it instead.
			return nil, sdk.Unavailable("seerr: the genre list is not available")
		}
	}
	for i, kind := range kinds {
		k := &pluginv1.DiscoverKind{Type: itemType(kind)}
		if len(genres[i]) > 0 {
			opt := &pluginv1.WorkOption{Key: keyGenre, Label: t.T("filterGenre"), Multiple: true}
			for _, g := range genres[i] {
				opt.Choices = append(opt.Choices, &pluginv1.WorkChoice{Value: strconv.Itoa(g.ID), Label: truncate(g.Name, 64)})
			}
			k.Filters = append(k.Filters, opt)
		}
		per := &pluginv1.WorkOption{Key: keyPeriod, Label: t.T("filterPeriod")}
		for _, pd := range periods {
			label := t.T("periodOlder")
			if pd.decade > 0 {
				label = t.Tf("periodDecade", pd.decade)
			}
			per.Choices = append(per.Choices, &pluginv1.WorkChoice{Value: pd.value, Label: label})
		}
		k.Filters = append(k.Filters, per)
		lang := &pluginv1.WorkOption{Key: keyLang, Label: t.T("filterLanguage")}
		for _, l := range origLangs {
			lang.Choices = append(lang.Choices, &pluginv1.WorkChoice{Value: l.value, Label: t.T(l.label)})
		}
		k.Filters = append(k.Filters, lang)
		rate := &pluginv1.WorkOption{Key: keyRating, Label: t.T("filterRating")}
		for _, r := range ratings {
			rate.Choices = append(rate.Choices, &pluginv1.WorkChoice{Value: r, Label: t.Tf("ratingAtLeast", r)})
		}
		k.Filters = append(k.Filters, rate)
		srt := &pluginv1.WorkOption{Key: keySort, Label: t.T("filterSort")}
		for _, s := range sorts {
			srt.Choices = append(srt.Choices, &pluginv1.WorkChoice{Value: s.value, Label: t.T(s.label), Selected: s.value == defaultSort})
		}
		k.Sort = srt
		info.Kinds = append(info.Kinds, k)
	}
	return info, nil
}

type genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// genreCache keeps Seerr's genre lists per type and language for ten minutes, and the last good list beyond that.
type genreCache struct {
	mu  sync.Mutex
	m   map[string]genreEntry
	ttl time.Duration
	now func() time.Time
}

type genreEntry struct {
	at   time.Time
	list []genre
}

func newGenreCache() *genreCache {
	return &genreCache{m: map[string]genreEntry{}, ttl: 10 * time.Minute, now: time.Now}
}

func (c *genreCache) clear() {
	c.mu.Lock()
	c.m = map[string]genreEntry{}
	c.mu.Unlock()
}

// list returns the genres of a type as Seerr names them in the member's language. When Seerr cannot be asked it returns the last good list,
// and without one the error (a fetch that works but gives no genres is an empty list, not an error).
func (c *genreCache) list(ctx context.Context, p *plugin, tg target, kind, tag string) ([]genre, error) {
	lang := tmdbLanguage(tag)
	key := kind + ":" + lang
	c.mu.Lock()
	e, ok := c.m[key]
	c.mu.Unlock()
	if ok && c.now().Sub(e.at) <= c.ttl {
		return e.list, nil
	}
	var gs []genre
	err := p.api.get(ctx, tg, "/genres/"+kind, url.Values{"language": {lang}}, &gs)
	if err != nil && ctx.Err() == nil {
		err = p.api.get(ctx, tg, "/genres/"+kind, url.Values{"language": {lang}}, &gs) // one more try: a failed answer must not be cached by the host
	}
	if err != nil {
		if ok {
			return e.list, nil // the last good list
		}
		slog.Warn("seerr genres failed", "kind", kind, "error", err.Error())
		return nil, err
	}
	out := make([]genre, 0, len(gs))
	for _, g := range gs {
		g.Name = strings.TrimSpace(g.Name)
		if g.ID <= 0 || g.Name == "" {
			continue
		}
		out = append(out, g)
	}
	c.mu.Lock()
	c.m[key] = genreEntry{at: c.now(), list: out}
	c.mu.Unlock()
	return out, nil
}
