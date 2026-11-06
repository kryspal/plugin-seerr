package main

import (
	"strings"
	"testing"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

func seedOf(t pluginv1.ItemType, tmdb string) *pluginv1.DiscoverSeed {
	return &pluginv1.DiscoverSeed{Type: t, ProviderIds: map[string]string{"tmdb": tmdb}}
}

var (
	movieT  = pluginv1.ItemType_ITEM_TYPE_MOVIE
	seriesT = pluginv1.ItemType_ITEM_TYPE_SERIES
)

func homeWithSeeds(t *testing.T, p *plugin, lang string, seeds ...*pluginv1.DiscoverSeed) *pluginv1.DiscoverPage {
	t.Helper()
	pg, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "home", Languages: langs(lang), Seeds: seeds})
	if err != nil {
		t.Fatal(err)
	}
	return pg
}

const matrixCollection = `{"id":2344,"name":"The Matrix Collection","parts":[
 {"id":604,"mediaType":"movie","title":"Reloaded","releaseDate":"2003-05-15","posterPath":"/b.jpg"},
 {"id":603,"mediaType":"movie","title":"The Matrix","releaseDate":"1999-03-31","posterPath":"/a.jpg"},
 {"id":605,"title":"Revolutions","releaseDate":"2003-11-05"}]}`

func setupSeeds(f *fakeSeerr) {
	f.details["movie:603"] = `{"id":603,"title":"The Matrix","collection":{"id":2344,"name":"The Matrix Collection"}}`
	f.details["movie:700"] = `{"id":700,"title":"Alone"}`
	f.details["tv:1396"] = `{"id":1396,"name":"Breaking Bad"}`
	f.collections = map[string]string{"2344": matrixCollection}
}

// Without seeds the page is the same as before: no row is made from them.
func TestNoSeedsNoSeedRows(t *testing.T) {
	p, f := newTestPlugin(t)
	setupSeeds(f)
	for _, seeds := range [][]*pluginv1.DiscoverSeed{nil, {}, {{Type: movieT, ProviderIds: map[string]string{"imdb": "tt1"}}}, {seedOf(movieT, "0"), seedOf(movieT, "x")}} {
		pg := homeWithSeeds(t, p, "en", seeds...)
		if got := rowIDs(pg); got != "hero,trending,popular_movies,popular_series,studios,networks" {
			t.Errorf("rows = %s", got)
		}
	}
	if n := f.count("/api/v1/collection/"); n != 0 {
		t.Errorf("%d collection calls without seeds", n)
	}
}

func TestSeedRows(t *testing.T) {
	p, f := newTestPlugin(t)
	setupSeeds(f)
	pg := homeWithSeeds(t, p, "zh-TW", seedOf(movieT, "603"), seedOf(movieT, "700"), seedOf(seriesT, "1396"))
	if got := rowIDs(pg); got != "hero,trending,collection_1,because_1,because_2,because_3,popular_movies,popular_series,studios,networks" {
		t.Fatalf("rows = %s", got)
	}
	col := rowByID(t, pg, "collection_1")
	if col.GetTitle() != "補齊系列：The Matrix Collection" || strings.Join(keysOf(col), ",") != "movie:603,movie:604,movie:605" {
		t.Errorf("collection = %q %v, want the whole collection, oldest first", col.GetTitle(), keysOf(col))
	}
	if b := rowByID(t, pg, "because_1"); b.GetTitle() != "因為你有 The Matrix" || len(b.GetWorks()) == 0 {
		t.Errorf("because_1 = %q %d works", b.GetTitle(), len(b.GetWorks()))
	}
	if b := rowByID(t, pg, "because_3"); b.GetTitle() != "因為你有 Breaking Bad" {
		t.Errorf("because_3 = %q", b.GetTitle())
	}
	// A row asked alone gives the same row for the same seeds.
	one, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "home", RowId: "because_2", Languages: langs("en"),
		Seeds: []*pluginv1.DiscoverSeed{seedOf(movieT, "603"), seedOf(movieT, "700"), seedOf(seriesT, "1396")}})
	if err != nil || len(one.GetRows()) != 1 || one.GetRows()[0].GetTitle() != "Because you have Alone" {
		t.Errorf("row alone = %v (%v)", one.GetRows(), err)
	}
	// Filters and sort do not depend on the seeds.
	plain := homeWithSeeds(t, p, "zh-TW")
	if len(pg.GetFilters()) != len(plain.GetFilters()) || (pg.GetSort() == nil) != (plain.GetSort() == nil) {
		t.Errorf("filters differ with seeds")
	}
}

// A seed that Seerr does not know costs only its row; the page stays whole.
func TestUnknownSeedsAreHarmless(t *testing.T) {
	p, _ := newTestPlugin(t)
	pg := homeWithSeeds(t, p, "en", seedOf(movieT, "1"), seedOf(seriesT, "1"))
	if got := rowIDs(pg); got != "hero,trending,popular_movies,popular_series,studios,networks" {
		t.Errorf("rows = %s", got)
	}
	one, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "home", RowId: "because_1", Languages: langs("en"), Seeds: []*pluginv1.DiscoverSeed{seedOf(movieT, "1")}})
	if err != nil || len(one.GetRows()) != 1 || len(one.GetRows()[0].GetWorks()) != 0 {
		t.Errorf("row alone = %v (%v)", one.GetRows(), err)
	}
}

// Rows are not repeated work for work: Seerr is asked about a seed once per request and remembered by number, not by set.
func TestSeedLookupsAreCachedByNumber(t *testing.T) {
	p, f := newTestPlugin(t)
	setupSeeds(f)
	homeWithSeeds(t, p, "en", seedOf(movieT, "603"), seedOf(movieT, "700"))
	first := f.countExact("/api/v1/movie/603")
	if first != 1 {
		t.Errorf("movie 603 was asked %d times in one request, want 1", first)
	}
	homeWithSeeds(t, p, "en", seedOf(movieT, "700"), seedOf(movieT, "603")) // another order, another set
	if n := f.countExact("/api/v1/movie/603"); n != 1 {
		t.Errorf("movie 603 asked again: %d", n)
	}
	if n := f.countExact("/api/v1/collection/2344"); n != 1 {
		t.Errorf("collection asked %d times, want 1", n)
	}
}

func TestOnlyDistinctCollectionsAndNoSingles(t *testing.T) {
	p, f := newTestPlugin(t)
	setupSeeds(f)
	f.details["movie:604"] = `{"id":604,"title":"Reloaded","collection":{"id":2344,"name":"The Matrix Collection"}}`
	f.details["movie:800"] = `{"id":800,"title":"One","collection":{"id":9,"name":"Lonely"}}`
	f.collections["9"] = `{"id":9,"name":"Lonely","parts":[{"id":800,"mediaType":"movie","title":"One","releaseDate":"2000-01-01"}]}`
	pg := homeWithSeeds(t, p, "en", seedOf(movieT, "603"), seedOf(movieT, "604"), seedOf(movieT, "800"))
	n := 0
	for _, r := range pg.GetRows() {
		if strings.HasPrefix(r.GetId(), "collection_") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d collection rows, want 1 (two seeds share one collection, and a collection of one is nothing to complete)", n)
	}
}

func TestSeedsOf(t *testing.T) {
	got := seedsOf([]*pluginv1.DiscoverSeed{seedOf(movieT, "5"), seedOf(movieT, "5"), seedOf(seriesT, "5"), seedOf(pluginv1.ItemType_ITEM_TYPE_UNSPECIFIED, "6"), seedOf(movieT, "-1")})
	if len(got) != 2 || got[0] != (seed{kindMovie, 5}) || got[1] != (seed{kindTV, 5}) {
		t.Errorf("seeds = %v", got)
	}
	var many []*pluginv1.DiscoverSeed
	for i := 1; i <= 40; i++ {
		many = append(many, seedOf(movieT, itoa(i)))
	}
	if n := len(seedsOf(many)); n != 20 {
		t.Errorf("%d seeds kept, want 20", n)
	}
}
