package main

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	pluginapi "github.com/kryspal/plugin-sdk"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

// Seeds (discover.seeds): up to 20 works of the library, sent by the host with every page request, only when the administrator allows it. They
// are used to answer the request and are never written down. A page works without them: the rows made from them are added when there are some
// and left out when there are none. The seeds change nothing about the filters or the sort, and what is cached is Seerr's answer by TMDB
// number, not the set of seeds.
//
//	collection_1, collection_2   the whole collection of a movie seed that belongs to one (the rest of a series of films)
//	because_1 .. because_3       what Seerr recommends after one of the first three seeds
//
// A row ID names a place in the seeds of the request, so the same seeds give the same IDs; with other seeds the row is another row.
const (
	seedTMDB       = "tmdb"
	maxBecauseRows = 3
	maxCollections = 2
	// lookAtSeeds is how many movie seeds are asked about for a collection.
	lookAtSeeds = 8
	seedInfoTTL = 30 * time.Minute
	seedInfoMax = 500
	collTTL     = 10 * time.Minute
	collMax     = 100
)

type seed struct {
	kind string
	id   int
}

// seedsOf keeps the seeds that carry a TMDB number, once each, in the order the host gave them.
func seedsOf(in []*pluginv1.DiscoverSeed) []seed {
	var out []seed
	seen := map[seed]bool{}
	for _, s := range in {
		kind := kindOf(s.GetType())
		id, ok := positive(s.GetProviderIds()[seedTMDB])
		if kind == "" || !ok || seen[seed{kind, id}] || len(out) >= pluginapi.MaxDiscoverSeeds {
			continue
		}
		seen[seed{kind, id}] = true
		out = append(out, seed{kind, id})
	}
	return out
}

// seedInfo is what a row needs to know about a seed: its title in the member's language, and for a movie the collection it is part of.
type seedInfo struct {
	at      time.Time
	title   string
	colID   int
	colName string
}

type seedCache struct {
	mu  sync.Mutex
	m   map[string]seedInfo
	col map[int]collEntry
	now func() time.Time
}

type collEntry struct {
	at    time.Time
	parts []result
}

func newSeedCache() *seedCache {
	return &seedCache{m: map[string]seedInfo{}, col: map[int]collEntry{}, now: time.Now}
}

func (c *seedCache) clear() {
	c.mu.Lock()
	c.m, c.col = map[string]seedInfo{}, map[int]collEntry{}
	c.mu.Unlock()
}

func (c *seedCache) info(key string) (seedInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	return e, ok && c.now().Sub(e.at) <= seedInfoTTL
}

func (c *seedCache) putInfo(key string, e seedInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= seedInfoMax {
		c.m = map[string]seedInfo{}
	}
	e.at = c.now()
	c.m[key] = e
}

func (c *seedCache) parts(id int) ([]result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.col[id]
	return e.parts, ok && c.now().Sub(e.at) <= collTTL
}

func (c *seedCache) putParts(id int, parts []result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.col) >= collMax {
		c.col = map[int]collEntry{}
	}
	c.col[id] = collEntry{at: c.now(), parts: parts}
}

// infoCall is one lookup shared by the rows of a request that need the same seed.
type infoCall struct {
	once sync.Once
	info seedInfo
	err  error
}

func (pc *pageCtx) infoOf(ctx context.Context, s seed) (seedInfo, error) {
	key := s.kind + ":" + strconv.Itoa(s.id) + ":" + tmdbLanguage(pc.tag)
	pc.seedMu.Lock()
	if pc.infos == nil {
		pc.infos = map[string]*infoCall{}
	}
	call := pc.infos[key]
	if call == nil {
		call = &infoCall{}
		pc.infos[key] = call
	}
	pc.seedMu.Unlock()
	call.once.Do(func() {
		if e, ok := pc.p.seeds.info(key); ok {
			call.info = e
			return
		}
		var r result
		if call.err = pc.p.api.get(ctx, pc.tg, "/"+s.kind+"/"+strconv.Itoa(s.id), mustQuery("language", tmdbLanguage(pc.tag)), &r); call.err != nil {
			return
		}
		name := r.Title
		if s.kind == kindTV {
			name = r.Name
		}
		call.info = seedInfo{title: truncate(name, 40)}
		if r.Collection != nil && r.Collection.ID > 0 {
			call.info.colID, call.info.colName = r.Collection.ID, truncate(r.Collection.Name, 40)
		}
		pc.p.seeds.putInfo(key, call.info)
	})
	return call.info, call.err
}

type collectionsCall struct {
	once sync.Once
	list []seedInfo
}

// collections is the distinct collections of the first movie seeds, in order, at most maxCollections.
func (pc *pageCtx) collections(ctx context.Context) []seedInfo {
	pc.seedMu.Lock()
	if pc.cols == nil {
		pc.cols = &collectionsCall{}
	}
	call := pc.cols
	pc.seedMu.Unlock()
	call.once.Do(func() {
		var movies []seed
		for _, s := range pc.seeds {
			if s.kind == kindMovie && len(movies) < lookAtSeeds {
				movies = append(movies, s)
			}
		}
		infos := make([]seedInfo, len(movies))
		oks := make([]bool, len(movies))
		var wg sync.WaitGroup
		for i, s := range movies {
			wg.Add(1)
			go func() {
				defer wg.Done()
				e, err := pc.infoOf(ctx, s)
				infos[i], oks[i] = e, err == nil
			}()
		}
		wg.Wait()
		seen := map[int]bool{}
		for i, e := range infos {
			if oks[i] && e.colID > 0 && !seen[e.colID] && len(call.list) < maxCollections {
				seen[e.colID] = true
				call.list = append(call.list, e)
			}
		}
	})
	return call.list
}

// seedRows are the rows made from the seeds: none without seeds.
func (pc *pageCtx) seedRows() []rowSpec {
	if len(pc.seeds) == 0 {
		return nil
	}
	var rows []rowSpec
	hasMovie := false
	for _, s := range pc.seeds {
		hasMovie = hasMovie || s.kind == kindMovie
	}
	if hasMovie {
		for i := 0; i < maxCollections; i++ {
			rows = append(rows, pc.collectionRow(i))
		}
	}
	for i := 0; i < len(pc.seeds) && i < maxBecauseRows; i++ {
		rows = append(rows, pc.becauseRow(i))
	}
	return rows
}

func seedRowID(prefix string, i int) string { return prefix + "_" + strconv.Itoa(i+1) }

// becauseRow is what Seerr recommends after the i-th seed, titled by that seed.
func (pc *pageCtx) becauseRow(i int) rowSpec {
	s := pc.seeds[i]
	id := seedRowID("because", i)
	return rowSpec{id: id, fetch: func(ctx context.Context, pageNo int) (*pluginv1.DiscoverRow, error) {
		empty := &pluginv1.DiscoverRow{Id: id, Title: pc.t.T("rowRecommended")}
		var info seedInfo
		var infoErr error
		var list *pluginv1.DiscoverRow
		var lerr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); info, infoErr = pc.infoOf(ctx, s) }()
		go func() {
			defer wg.Done()
			list, lerr = pc.listRow(id, "", "/"+s.kind+"/"+strconv.Itoa(s.id)+"/recommendations", nil, s.kind, pluginv1.DiscoverRowStyle_DISCOVER_ROW_STYLE_POSTERS, "").fetch(ctx, pageNo)
		}()
		wg.Wait()
		if infoErr != nil || info.title == "" || lerr != nil {
			if ctx.Err() == nil {
				slog.Debug("a row after a seed was left out", "row", id)
			}
			return empty, nil // a seed Seerr does not know (or cannot answer for now) only costs its row
		}
		list.Title = truncate(pc.t.Tf("rowBecause", info.title), pluginapi.MaxWorkRowTitle)
		return list, nil
	}}
}

// collectionRow is the whole collection of the i-th distinct collection among the movie seeds, oldest first.
func (pc *pageCtx) collectionRow(i int) rowSpec {
	id := seedRowID("collection", i)
	return rowSpec{id: id, keepAll: true, fetch: func(ctx context.Context, _ int) (*pluginv1.DiscoverRow, error) {
		empty := &pluginv1.DiscoverRow{Id: id, Title: pc.t.T("rowCollection")}
		cols := pc.collections(ctx)
		if i >= len(cols) {
			return empty, nil
		}
		col := cols[i]
		parts, ok := pc.p.seeds.parts(col.colID)
		if !ok {
			var c struct {
				Parts []result `json:"parts"`
			}
			if err := pc.p.api.get(ctx, pc.tg, "/collection/"+strconv.Itoa(col.colID), nil, &c); err != nil {
				return empty, nil
			}
			parts = c.Parts
			pc.p.seeds.putParts(col.colID, parts)
			pc.p.states.record(parts)
		}
		parts = append([]result(nil), parts...) // the cached list is shared
		sort.SliceStable(parts, func(a, b int) bool {
			x, y := parts[a].ReleaseDate, parts[b].ReleaseDate
			switch {
			case x == "":
				return false
			case y == "":
				return true
			}
			return x < y
		})
		ws := works(parts, kindMovie, pc.t)
		if len(ws) < 2 {
			return empty, nil // a collection of one is nothing to complete
		}
		return &pluginv1.DiscoverRow{Id: id, Title: truncate(pc.t.Tf("rowCollectionOf", col.colName), pluginapi.MaxWorkRowTitle), Works: ws}, nil
	}}
}
