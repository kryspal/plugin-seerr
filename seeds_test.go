package main

import (
	"strings"
	"testing"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	movieT   = pluginv1.ItemType_ITEM_TYPE_MOVIE
	seriesT  = pluginv1.ItemType_ITEM_TYPE_SERIES
	watchedO = pluginv1.DiscoverSeedOrigin_DISCOVER_SEED_ORIGIN_WATCHED
	libraryO = pluginv1.DiscoverSeedOrigin_DISCOVER_SEED_ORIGIN_LIBRARY
)

func seedFrom(o pluginv1.DiscoverSeedOrigin, t pluginv1.ItemType, tmdb string) *pluginv1.DiscoverSeed {
	return &pluginv1.DiscoverSeed{Origin: o, Type: t, ProviderIds: map[string]string{"tmdb": tmdb}}
}

func forYou(t *testing.T, p *plugin, batch uint32, row string, seeds ...*pluginv1.DiscoverSeed) *pluginv1.DiscoverPage {
	t.Helper()
	pg, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "foryou", RowId: row, Batch: batch, Languages: langs("zh-TW"), Seeds: seeds})
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

func TestForYouIsASeededTabAfterTheFirst(t *testing.T) {
	p, _ := newTestPlugin(t)
	info, err := p.DescribeDiscoverSource(ctx, &pluginv1.DescribeDiscoverSourceRequest{Languages: langs("en")})
	if err != nil {
		t.Fatal(err)
	}
	tabs := info.GetTabs()
	last := tabs[len(tabs)-1]
	if !last.GetSeeded() || last.GetRef() != "foryou" || last.GetTitle() != "For you" {
		t.Errorf("last tab = %v", last)
	}
	for _, tab := range tabs[:len(tabs)-1] {
		if tab.GetSeeded() {
			t.Errorf("tab %q is seeded", tab.GetTitle())
		}
	}
	if tabs[0].GetSeeded() {
		t.Errorf("the first tab cannot be seeded")
	}
}

// The home page has no row made from seeds, whatever it is sent.
func TestHomeIgnoresSeeds(t *testing.T) {
	p, f := newTestPlugin(t)
	setupSeeds(f)
	pg, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "home", Languages: langs("en"), Seeds: []*pluginv1.DiscoverSeed{seedFrom(watchedO, movieT, "603")}})
	if err != nil || rowIDs(pg) != "hero,trending,popular_movies,popular_series,studios,networks" {
		t.Errorf("rows = %s (%v)", rowIDs(pg), err)
	}
}

// Without seeds the tab is an empty page, which the host never shows.
func TestForYouWithoutSeeds(t *testing.T) {
	p, f := newTestPlugin(t)
	setupSeeds(f)
	for _, seeds := range [][]*pluginv1.DiscoverSeed{nil, {{Origin: watchedO, Type: movieT, ProviderIds: map[string]string{"imdb": "tt1"}}}} {
		pg, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "foryou", Languages: langs("en"), Seeds: seeds})
		if err != nil || len(pg.GetRows()) != 0 {
			t.Errorf("rows = %s (%v)", rowIDs(pg), err)
		}
	}
	if _, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "foryou", RowId: "for_you", Languages: langs("en")}); status.Code(err) != codes.NotFound {
		t.Logf("a row asked for with no seeds: %v", err)
	}
}

func TestForYouPage(t *testing.T) {
	p, f := newTestPlugin(t)
	setupSeeds(f)
	pg := forYou(t, p, 0, "", seedFrom(watchedO, movieT, "603"), seedFrom(watchedO, seriesT, "1396"), seedFrom(libraryO, movieT, "700"))
	if got := rowIDs(pg); got != "collection_1,for_you" {
		t.Fatalf("rows = %s", got)
	}
	col := rowByID(t, pg, "collection_1")
	if col.GetTitle() != "補齊系列：The Matrix Collection" || strings.Join(keysOf(col), ",") != "movie:603,movie:604,movie:605" {
		t.Errorf("collection = %q %v", col.GetTitle(), keysOf(col))
	}
	fy := rowByID(t, pg, "for_you")
	if !fy.GetRefreshable() || fy.GetTitle() != "為你推薦" || len(fy.GetWorks()) == 0 || fy.GetNextPageToken() != "" {
		t.Errorf("for_you = %q refreshable %v, %d works", fy.GetTitle(), fy.GetRefreshable(), len(fy.GetWorks()))
	}
	for _, w := range fy.GetWorks() {
		for _, name := range []string{"The Matrix", "Breaking Bad", "Alone"} {
			if strings.Contains(fy.GetTitle(), name) || strings.Contains(w.GetCaption(), name) {
				t.Errorf("the row names %s", name)
			}
		}
		if w.GetKey() == "movie:603" || w.GetKey() == "tv:1396" {
			t.Errorf("a seed is recommended after itself: %s", w.GetKey())
		}
	}
	// The same batch alone, and with the page, gives the same works.
	again := forYou(t, p, 0, "for_you", seedFrom(watchedO, movieT, "603"), seedFrom(watchedO, seriesT, "1396"), seedFrom(libraryO, movieT, "700"))
	if len(again.GetRows()) != 1 || strings.Join(keysOf(again.GetRows()[0]), ",") != strings.Join(keysOf(fy), ",") {
		t.Errorf("batch 0 differs between asks")
	}
}

// Watched works come before the library's for collections, each collection once, and the library fills in.
func TestCollectionsWatchedFirstThenLibrary(t *testing.T) {
	p, f := newTestPlugin(t)
	setupSeeds(f)
	f.details["movie:800"] = `{"id":800,"title":"Heat","collection":{"id":9,"name":"Heat Collection"}}`
	f.details["movie:604"] = `{"id":604,"title":"Reloaded","collection":{"id":2344,"name":"The Matrix Collection"}}`
	f.collections["9"] = `{"id":9,"name":"Heat Collection","parts":[{"id":800,"mediaType":"movie","title":"Heat","releaseDate":"1995-01-01","posterPath":"/h.jpg"},{"id":801,"mediaType":"movie","title":"Heat 2","releaseDate":"2030-01-01","posterPath":"/h2.jpg"}]}`
	f.details["movie:900"] = `{"id":900,"title":"Solo","collection":{"id":77,"name":"Solo Collection"}}`
	f.collections["77"] = `{"id":77,"name":"Solo Collection","parts":[{"id":900,"mediaType":"movie","title":"Solo","releaseDate":"2001-01-01"}]}`
	pg := forYou(t, p, 1, "", seedFrom(libraryO, movieT, "800"), seedFrom(watchedO, movieT, "604"), seedFrom(watchedO, movieT, "603"), seedFrom(libraryO, movieT, "900"))
	var cols []string
	for _, r := range pg.GetRows() {
		if strings.HasPrefix(r.GetId(), "collection_") {
			cols = append(cols, r.GetTitle())
		}
	}
	if want := "補齊系列：The Matrix Collection,補齊系列：Heat Collection"; strings.Join(cols, ",") != want {
		t.Errorf("collections = %v, want the watched one first, once, then the library's; a collection of one is left out", cols)
	}
}

// Another batch is another draw: over many batches the row varies a lot and a batch is stable.
func TestBatchesDiffer(t *testing.T) {
	p, _ := newTestPlugin(t)
	var seeds []*pluginv1.DiscoverSeed
	for i := 1; i <= 12; i++ {
		seeds = append(seeds, seedFrom(watchedO, movieT, itoa(i)))
	}
	seen := map[string]bool{}
	var firstKeys []string
	for b := uint32(0); b < 12; b++ {
		fy := forYou(t, p, b, "for_you", seeds...).GetRows()[0]
		if b == 0 {
			firstKeys = keysOf(fy)
		}
		for _, k := range keysOf(fy) {
			seen[k] = true
		}
	}
	if len(firstKeys) == 0 {
		t.Fatal("batch 0 is empty")
	}
	if len(seen) <= len(firstKeys) {
		t.Errorf("12 batches gave %d distinct works, batch 0 alone %d", len(seen), len(firstKeys))
	}
	if again := keysOf(forYou(t, p, 0, "for_you", seeds...).GetRows()[0]); strings.Join(again, ",") != strings.Join(firstKeys, ",") {
		t.Errorf("batch 0 is not stable")
	}
	// The biggest number is a batch like any other.
	if len(forYou(t, p, 4294967295, "for_you", seeds...).GetRows()[0].GetWorks()) == 0 {
		t.Errorf("the largest batch is empty")
	}
}

// A seed Seerr does not know costs only its share; with nothing known the row is empty, not an error.
func TestUnknownSeedsAreHarmless(t *testing.T) {
	p, f := newTestPlugin(t)
	f.list = ""
	pg := forYou(t, p, 3, "", seedFrom(watchedO, movieT, "1"), seedFrom(libraryO, seriesT, "1"))
	if len(pg.GetRows()) > 2 {
		t.Errorf("rows = %s", rowIDs(pg))
	}
}

func TestSeedsOf(t *testing.T) {
	w, l := seedsOf([]*pluginv1.DiscoverSeed{seedFrom(watchedO, movieT, "5"), seedFrom(watchedO, movieT, "5"), seedFrom(libraryO, seriesT, "5"),
		seedFrom(libraryO, pluginv1.ItemType_ITEM_TYPE_UNSPECIFIED, "6"), seedFrom(watchedO, movieT, "-1"), seedFrom(pluginv1.DiscoverSeedOrigin_DISCOVER_SEED_ORIGIN_UNSPECIFIED, movieT, "7")})
	if len(w) != 1 || w[0] != (seed{kindMovie, 5}) || len(l) != 2 || l[0] != (seed{kindTV, 5}) || l[1] != (seed{kindMovie, 7}) {
		t.Errorf("watched %v library %v", w, l)
	}
	var many []*pluginv1.DiscoverSeed
	for i := 1; i <= 80; i++ {
		many = append(many, seedFrom(watchedO, movieT, itoa(i)), seedFrom(libraryO, movieT, itoa(1000+i)))
	}
	if w, l := seedsOf(many); len(w) != 30 || len(l) != 20 {
		t.Errorf("%d watched and %d library kept, want 30 and 20", len(w), len(l))
	}
}

// Seerr is asked about a seed once per request and remembered by number, not by set.
func TestSeedLookupsAreCachedByNumber(t *testing.T) {
	p, f := newTestPlugin(t)
	setupSeeds(f)
	forYou(t, p, 0, "", seedFrom(watchedO, movieT, "603"), seedFrom(watchedO, movieT, "700"))
	forYou(t, p, 1, "", seedFrom(watchedO, movieT, "700"), seedFrom(watchedO, movieT, "603"))
	if n := f.countExact("/api/v1/movie/603"); n != 1 {
		t.Errorf("movie 603 was asked %d times, want 1", n)
	}
	if n := f.countExact("/api/v1/collection/2344"); n != 1 {
		t.Errorf("collection asked %d times, want 1", n)
	}
}
