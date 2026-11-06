package main

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kryspal/plugin-sdk/i18n"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
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

// ---- the controls of a page ----

// filterOptions declares the filters and the sort of a page of movies or of series: the genres when there are any, then period, original
// language and rating, and the sort. A page of one genre passes no genres.
func filterOptions(t i18n.Translator, genres []genre) (filters []*pluginv1.WorkOption, sort *pluginv1.WorkOption) {
	if len(genres) > 0 {
		opt := &pluginv1.WorkOption{Key: keyGenre, Label: t.T("filterGenre"), Multiple: true}
		for _, g := range genres {
			opt.Choices = append(opt.Choices, &pluginv1.WorkChoice{Value: strconv.Itoa(g.ID), Label: truncate(g.Name, 64)})
		}
		filters = append(filters, opt)
	}
	per := &pluginv1.WorkOption{Key: keyPeriod, Label: t.T("filterPeriod")}
	for _, pd := range periods {
		label := t.T("periodOlder")
		if pd.decade > 0 {
			label = t.Tf("periodDecade", pd.decade)
		}
		per.Choices = append(per.Choices, &pluginv1.WorkChoice{Value: pd.value, Label: label})
	}
	lang := &pluginv1.WorkOption{Key: keyLang, Label: t.T("filterLanguage")}
	for _, l := range origLangs {
		lang.Choices = append(lang.Choices, &pluginv1.WorkChoice{Value: l.value, Label: t.T(l.label)})
	}
	rate := &pluginv1.WorkOption{Key: keyRating, Label: t.T("filterRating")}
	for _, r := range ratings {
		rate.Choices = append(rate.Choices, &pluginv1.WorkChoice{Value: r, Label: t.Tf("ratingAtLeast", r)})
	}
	sort = &pluginv1.WorkOption{Key: keySort, Label: t.T("filterSort")}
	for _, s := range sorts {
		sort.Choices = append(sort.Choices, &pluginv1.WorkChoice{Value: s.value, Label: t.T(s.label), Selected: s.value == defaultSort})
	}
	return append(filters, per, lang, rate), sort
}

type genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// genreCache keeps Seerr's genre lists per type and language for ten minutes, and the last good list beyond that.
type genreCache struct {
	mu      sync.Mutex
	m       map[string]genreEntry
	cards   map[string]cardEntry
	flights map[string]chan struct{}
	ttl     time.Duration
	now     func() time.Time
}

type genreEntry struct {
	at   time.Time
	list []genre
}

func newGenreCache() *genreCache {
	return &genreCache{m: map[string]genreEntry{}, cards: map[string]cardEntry{}, flights: map[string]chan struct{}{}, ttl: 10 * time.Minute, now: time.Now}
}

func (c *genreCache) clear() {
	c.mu.Lock()
	c.m = map[string]genreEntry{}
	c.cards = map[string]cardEntry{}
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

// genreCard is one entry of the genre wall: the genre and one of its backdrops.
type genreCard struct {
	genre
	Backdrop string
}

type cardEntry struct {
	at       time.Time
	list     []genreCard
	degraded bool // the slider failed or some card has no picture: kept only briefly, so the wall heals when Seerr can give them
}

// degradedTTL is how long a wall with missing pictures is kept; wallWait is how long a page waits for the slider before it shows the genres
// without pictures. Seerr answers the slider only after one call to TMDB per genre, which can take many seconds on a cold start: the call runs
// on its own (long timeout, one at a time per type and language, not cancelled when the page gives up), and the next page has its result.
var (
	degradedTTL = 20 * time.Second
	wallWait    = 4 * time.Second
)

type sliderGenre struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	Backdrops []string `json:"backdrops"`
}

// wall returns the genres of a type with a backdrop each (Seerr's genre slider, kept ten minutes). When the slider is not there in time the
// genres come without pictures and the slider goes on in the background; when no genres can be read at all, the error.
func (c *genreCache) wall(ctx context.Context, p *plugin, tg target, kind, tag string) ([]genreCard, error) {
	lang := tmdbLanguage(tag)
	key := kind + ":" + lang
	if l, ok := c.freshWall(key); ok {
		return l, nil
	}
	done := c.startSlider(p, tg, kind, lang, key)
	select {
	case <-done:
	case <-time.After(wallWait):
	case <-ctx.Done():
	}
	c.mu.Lock()
	e, ok := c.cards[key]
	c.mu.Unlock()
	if ok && len(e.list) > 0 {
		return e.list, nil // possibly degraded: some cards without a picture
	}
	gs, err := c.list(ctx, p, tg, kind, tag)
	if err != nil {
		return nil, err
	}
	out := make([]genreCard, 0, len(gs))
	for _, g := range gs {
		out = append(out, genreCard{genre: g})
	}
	return out, nil
}

func (c *genreCache) freshWall(key string) ([]genreCard, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cards[key]
	if !ok || len(e.list) == 0 {
		return nil, false
	}
	ttl := c.ttl
	if e.degraded {
		ttl = degradedTTL
	}
	return e.list, c.now().Sub(e.at) <= ttl
}

// startSlider starts the slider call unless one is running and returns a channel that closes when it ends.
func (c *genreCache) startSlider(p *plugin, tg target, kind, lang, key string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.flights[key]; ok {
		return ch
	}
	ch := make(chan struct{})
	c.flights[key] = ch
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.flights, key)
			c.mu.Unlock()
			close(ch)
		}()
		var slider []sliderGenre
		started := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), slowTimeout)
		defer cancel()
		if err := p.api.getSlow(ctx, tg, "/discover/genreslider/"+kind, url.Values{"language": {lang}}, &slider); err != nil {
			slog.Warn("seerr genre slider failed", "kind", kind, "after", time.Since(started).Round(time.Millisecond).String(), "error", err.Error())
			return
		}
		var out []genreCard
		degraded := false
		for _, g := range slider {
			if name := strings.TrimSpace(g.Name); g.ID > 0 && name != "" {
				gc := genreCard{genre: genre{ID: g.ID, Name: name}}
				gc.Backdrop = genreImage(g.ID, g.Backdrops)
				degraded = degraded || gc.Backdrop == ""
				out = append(out, gc)
			}
		}
		if degraded {
			slog.Warn("seerr genre slider has genres without a picture", "kind", kind)
		}
		slog.Debug("seerr genre slider", "kind", kind, "took", time.Since(started).Round(time.Millisecond).String(), "genres", len(out))
		c.mu.Lock()
		c.cards[key] = cardEntry{at: c.now(), list: out, degraded: degraded}
		c.mu.Unlock()
	}()
	return ch
}

// Seerr shows a genre on a duotone picture: TMDB's image service recolours the backdrop between two colours that belong to the genre,
// and Seerr takes the fifth backdrop of the slider at 1280 px. The wall does the same, with the first backdrop when there are fewer.
var genreTones = map[int][2]string{
	28: {"991B1B", "FCA5A5"}, 12: {"480c8b", "a96bef"}, 16: {"032541", "01b4e4"}, 35: {"92400E", "FCD34D"},
	80: {"1F2937", "2864d2"}, 99: {"065F46", "6EE7B7"}, 18: {"9D174D", "F9A8D4"}, 10751: {"777e0d", "e4ed55"},
	14: {"1F2937", "60A5FA"}, 36: {"92400E", "FCD34D"}, 27: {"1F2937", "D1D5DB"}, 10402: {"032541", "01b4e4"},
	9648: {"5B21B6", "C4B5FD"}, 10749: {"9D174D", "F9A8D4"}, 878: {"1F2937", "60A5FA"}, 10770: {"991B1B", "FCA5A5"},
	53: {"1F2937", "D1D5DB"}, 10752: {"1F2937", "F87171"}, 37: {"92400E", "FCD34D"}, 10759: {"480c8b", "a96bef"},
	10762: {"032541", "01b4e4"}, 10763: {"1F2937", "D1D5DB"}, 10764: {"552c01", "d47c1d"}, 10765: {"1F2937", "60A5FA"},
	10766: {"9D174D", "F9A8D4"}, 10767: {"065F46", "6EE7B7"}, 10768: {"1F2937", "F87171"},
}

func genreImage(id int, backdrops []string) string {
	path := ""
	if len(backdrops) > 4 && backdrops[4] != "" {
		path = backdrops[4]
	} else if len(backdrops) > 0 {
		path = backdrops[0]
	}
	if path == "" {
		return ""
	}
	tone, ok := genreTones[id]
	if !ok {
		tone = [2]string{"1F2937", "D1D5DB"}
	}
	return imageBase + "w1280_filter(duotone," + tone[0] + "," + tone[1] + ")" + path
}
