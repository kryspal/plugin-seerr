package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	pluginapi "github.com/kryspal/plugin-sdk"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

// Seeds (discover.seeds): library works and works the member watched, sent by the host only with requests for the "For you" tab (the tab is
// declared seeded). They are used to answer the request and are never written down. The tab works without them (it is empty, and the host
// does not show it then). The seeds change nothing about the filters or the sort, and what is cached is Seerr's answer by TMDB number, not the
// set of seeds.
//
//	collection_1 .. collection_3   the whole collection of a seed that belongs to one, the watched ones first, each collection once
//	for_you                        recommendations after a few of the watched seeds (the library's when nothing was watched), chosen at
//	                               random by the batch number; refreshable, so the viewer can ask for another batch. The title names no film.
//
// A row ID names a place in the request, so the same seeds give the same IDs.
const (
	seedTMDB       = "tmdb"
	maxCollections = 3
	// lookAtSeeds is how many movie seeds are asked about for a collection.
	lookAtSeeds = 30
	seedInfoTTL = 30 * time.Minute
	seedInfoMax = 500
	collTTL     = 10 * time.Minute
	collMax     = 100

	forYouSeeds   = 4  // seeds one batch draws recommendations from
	forYouDeep    = 10 // how deep into a seed's recommendations a batch may go (pages of 20)
	forYouPerSeed = 10 // works one seed adds to a batch
	forYouMax     = 40
)

type seed struct {
	kind string
	id   int
}

// seedsOf splits the seeds by origin, keeping those with a TMDB number, once each, in the order the host gave them. A work that is in both
// lists stays in each.
func seedsOf(in []*pluginv1.DiscoverSeed) (watched, library []seed) {
	seen := map[seed]bool{}
	seenW := map[seed]bool{}
	for _, s := range in {
		kind := kindOf(s.GetType())
		id, ok := positive(s.GetProviderIds()[seedTMDB])
		if kind == "" || !ok {
			continue
		}
		k := seed{kind, id}
		if s.GetOrigin() == pluginv1.DiscoverSeedOrigin_DISCOVER_SEED_ORIGIN_WATCHED {
			if !seenW[k] && len(watched) < pluginapi.MaxWatchedSeeds {
				seenW[k] = true
				watched = append(watched, k)
			}
			continue
		}
		if !seen[k] && len(library) < pluginapi.MaxLibrarySeeds {
			seen[k] = true
			library = append(library, k)
		}
	}
	return watched, library
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

// collections is the distinct collections of the movie seeds, the watched ones first, at most maxCollections.
func (pc *pageCtx) collections(ctx context.Context) []seedInfo {
	pc.seedMu.Lock()
	if pc.cols == nil {
		pc.cols = &collectionsCall{}
	}
	call := pc.cols
	pc.seedMu.Unlock()
	call.once.Do(func() {
		var movies []seed
		seen := map[seed]bool{}
		for _, list := range [][]seed{pc.watched, pc.library} {
			for _, s := range list {
				if s.kind == kindMovie && !seen[s] && len(movies) < lookAtSeeds {
					seen[s] = true
					movies = append(movies, s)
				}
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
		have := map[int]bool{}
		for i, e := range infos {
			if oks[i] && e.colID > 0 && !have[e.colID] && len(call.list) < maxCollections {
				have[e.colID] = true
				call.list = append(call.list, e)
			}
		}
	})
	return call.list
}

func seedRowID(prefix string, i int) string { return prefix + "_" + strconv.Itoa(i+1) }

// recommendRows are the rows of the "For you" tab: none without seeds.
func (pc *pageCtx) recommendRows() []rowSpec {
	if len(pc.watched)+len(pc.library) == 0 {
		return nil
	}
	rows := []rowSpec{pc.forYouRow()}
	for i := 0; i < maxCollections; i++ {
		rows = append(rows, pc.collectionRow(i))
	}
	return rows
}

// forYouRow is a batch of recommendations: a few seeds drawn at random by the batch number, and from each a random page of what Seerr
// recommends after it. The same batch and seeds give the same row; another batch gives another one. The title names no work.
func (pc *pageCtx) forYouRow() rowSpec {
	const id = "for_you"
	return rowSpec{id: id, fetch: func(ctx context.Context, _ int) (*pluginv1.DiscoverRow, error) {
		row := &pluginv1.DiscoverRow{Id: id, Title: pc.t.T("rowForYou"), Refreshable: true}
		pool := append([]seed(nil), pc.watched...)
		if len(pool) == 0 {
			pool = append(pool, pc.library...)
		}
		// The host gives no order, so the draw starts from a fixed one: the same batch then means the same row.
		sort.Slice(pool, func(a, b int) bool {
			if pool[a].kind != pool[b].kind {
				return pool[a].kind < pool[b].kind
			}
			return pool[a].id < pool[b].id
		})
		rng := rand.New(rand.NewPCG(uint64(pc.batch)+1, 0x9E3779B97F4A7C15))
		rng.Shuffle(len(pool), func(a, b int) { pool[a], pool[b] = pool[b], pool[a] })
		if len(pool) > forYouSeeds {
			pool = pool[:forYouSeeds]
		}
		rngs := make([]*rand.Rand, len(pool))
		for i := range pool {
			rngs[i] = rand.New(rand.NewPCG(rng.Uint64(), rng.Uint64()))
		}
		skip := map[string]bool{}
		for _, list := range [][]seed{pc.watched, pc.library} {
			for _, s := range list {
				skip[workKey(s.kind, s.id)] = true
			}
		}
		drawn := make([][]*pluginv1.Work, len(pool))
		var wg sync.WaitGroup
		for i, s := range pool {
			wg.Add(1)
			go func() {
				defer wg.Done()
				drawn[i] = pc.draw(ctx, s, rngs[i], skip)
			}()
		}
		wg.Wait()
		// One work from each seed in turn, so the row mixes them.
		seen := map[string]bool{}
		for n := 0; len(row.Works) < forYouMax; n++ {
			more := false
			for _, ws := range drawn {
				if n >= len(ws) {
					continue
				}
				more = true
				if w := ws[n]; !seen[w.GetKey()] && len(row.Works) < forYouMax {
					seen[w.GetKey()] = true
					row.Works = append(row.Works, w)
				}
			}
			if !more {
				break
			}
		}
		return row, nil
	}}
}

// draw is a random page of what Seerr recommends after a seed, shuffled, without the seeds themselves and without titles that have no poster.
// A seed Seerr cannot answer for gives nothing.
func (pc *pageCtx) draw(ctx context.Context, s seed, rng *rand.Rand, skip map[string]bool) []*pluginv1.Work {
	path := "/" + s.kind + "/" + strconv.Itoa(s.id) + "/recommendations"
	first, err := pc.p.page(ctx, pc.tg, path, url.Values{"page": {"1"}})
	if err != nil {
		if ctx.Err() == nil {
			slog.Debug("recommendations of a seed were left out", "error", err.Error())
		}
		return nil
	}
	pg := first
	if deep := min(first.TotalPages, forYouDeep); deep > 1 {
		if n := 1 + rng.IntN(deep); n > 1 {
			if p2, err := pc.p.page(ctx, pc.tg, path, url.Values{"page": {strconv.Itoa(n)}}); err == nil {
				pg = p2
			}
		}
	}
	var rs []result
	for _, r := range pg.Results {
		if r.PosterPath != "" && !skip[workKey(s.kind, r.ID)] {
			rs = append(rs, r)
		}
	}
	rng.Shuffle(len(rs), func(a, b int) { rs[a], rs[b] = rs[b], rs[a] })
	if len(rs) > forYouPerSeed {
		rs = rs[:forYouPerSeed]
	}
	return works(rs, s.kind, pc.t)
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
