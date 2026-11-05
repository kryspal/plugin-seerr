package main

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kryspal/plugin-sdk/hanconv"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

// The keys of the filters and the sort a kind declares. They are the same for movies and series.
const (
	keyGenre  = "genre"
	keyPeriod = "period"
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
	if v := sel[keyGenre]; len(v) == 1 {
		if n, err := strconv.Atoi(v[0]); err == nil && n > 0 {
			q.Set("genre", v[0])
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
	if n := s.minVotes[kind]; n > 0 {
		q.Set("voteCountGte", strconv.Itoa(n))
	}
	return s.value
}

// ---- DescribeDiscoverSource ----

// DescribeDiscoverSource declares the two types and, for each, the genre and period filters and the sort. The genres come from Seerr and are
// kept for ten minutes; when Seerr cannot be asked, the last list is used, and without one the genre filter is left out (the source stays up).
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
	var wg sync.WaitGroup
	for i, kind := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			genres[i] = p.genres.list(ctx, p, tg, kind, tag)
		}()
	}
	wg.Wait()
	for i, kind := range kinds {
		k := &pluginv1.DiscoverKind{Type: itemType(kind)}
		if len(genres[i]) > 0 {
			opt := &pluginv1.WorkOption{Key: keyGenre, Label: t.T("filterGenre")}
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

// list returns the genres of a type in the member's language; for Traditional Chinese, Seerr's Simplified names are converted (TMDB has no
// Traditional genre names). Nil when there is no list at all.
func (c *genreCache) list(ctx context.Context, p *plugin, tg target, kind, tag string) []genre {
	lang := tmdbLanguage(tag)
	key := kind + ":" + lang
	c.mu.Lock()
	e, ok := c.m[key]
	c.mu.Unlock()
	if ok && c.now().Sub(e.at) <= c.ttl {
		return e.list
	}
	var gs []genre
	if err := p.api.get(ctx, tg, "/genres/"+kind, url.Values{"language": {lang}}, &gs); err != nil {
		return e.list // the last good list, if any
	}
	{
		conv := traditionalConverter(tag)
		out := make([]genre, 0, len(gs))
		for _, g := range gs {
			g.Name = strings.TrimSpace(g.Name)
			if g.ID <= 0 || g.Name == "" {
				continue
			}
			if conv != nil {
				g.Name = conv.Convert(g.Name)
			}
			out = append(out, g)
		}
		c.mu.Lock()
		c.m[key] = genreEntry{at: c.now(), list: out}
		c.mu.Unlock()
		return out
	}
}

// traditionalConverter returns the Simplified-to-Traditional converter for a Traditional Chinese tag (Taiwan phrases for zh-TW, Hong Kong forms
// for zh-HK, plain for zh-Hant and zh-MO); nil for every other language.
func traditionalConverter(tag string) *hanconv.Converter {
	name := ""
	switch strings.ToLower(strings.ReplaceAll(tag, "_", "-")) {
	case "zh-tw", "zh-hant-tw":
		name = "s2twp"
	case "zh-hk", "zh-hant-hk":
		name = "s2hk"
	case "zh-hant", "zh-mo", "zh-hant-mo":
		name = "s2t"
	default:
		return nil
	}
	convMu.Lock()
	defer convMu.Unlock()
	if c, ok := convs[name]; ok {
		return c
	}
	c, err := hanconv.New(name)
	if err != nil {
		return nil
	}
	convs[name] = c
	return c
}

var (
	convMu sync.Mutex
	convs  = map[string]*hanconv.Converter{}
)
