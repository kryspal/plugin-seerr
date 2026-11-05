package main

import (
	"net/url"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func getPage(t *testing.T, p *plugin, ref, row, token, lang string, sels ...*pluginv1.WorkSelection) *pluginv1.DiscoverPage {
	t.Helper()
	pg, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: ref, RowId: row, PageToken: token, Languages: langs(lang), Selections: sels})
	if err != nil {
		t.Fatalf("GetDiscoverPage(%s, %s): %v", ref, row, err)
	}
	return pg
}

func rowIDs(pg *pluginv1.DiscoverPage) string {
	var ids []string
	for _, r := range pg.GetRows() {
		ids = append(ids, r.GetId())
	}
	return strings.Join(ids, ",")
}

func rowByID(t *testing.T, pg *pluginv1.DiscoverPage, id string) *pluginv1.DiscoverRow {
	t.Helper()
	for _, r := range pg.GetRows() {
		if r.GetId() == id {
			return r
		}
	}
	t.Fatalf("no row %q in %s", id, rowIDs(pg))
	return nil
}

func TestDescribeDeclaresTabsAndSearch(t *testing.T) {
	p, _ := newTestPlugin(t)
	info, err := p.DescribeDiscoverSource(ctx, &pluginv1.DescribeDiscoverSourceRequest{Languages: langs("zh-TW")})
	if err != nil {
		t.Fatal(err)
	}
	var tabs []string
	for _, tab := range info.GetTabs() {
		tabs = append(tabs, tab.GetTitle()+"="+tab.GetRef())
	}
	if got := strings.Join(tabs, ","); got != "首頁=home,電影=movies,劇集=series,類型=genres" {
		t.Errorf("tabs = %s", got)
	}
	if len(info.GetSearchTypes()) != 2 {
		t.Errorf("search types = %v", info.GetSearchTypes())
	}
	for _, tab := range info.GetTabs() {
		if _, ok := parseRef(tab.GetRef()); !ok {
			t.Errorf("tab ref %q does not parse", tab.GetRef())
		}
	}
}

func TestUnknownRefsAreNotFound(t *testing.T) {
	p, _ := newTestPlugin(t)
	for _, ref := range []string{"plugintest-no-such-ref", "genre:movie:x", "genre:radio:1", "genre:movie:0", "studio:", "person:-1", "rec:movie", "genre:movie:28:1", "HOME"} {
		if _, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: ref}); status.Code(err) != codes.NotFound {
			t.Errorf("ref %q: %v, want NOT_FOUND", ref, err)
		}
	}
	if _, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "home", RowId: "nope"}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown row: %v", err)
	}
	if _, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "home", RowId: "trending", PageToken: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("bad token: %v", err)
	}
}

func TestHomePage(t *testing.T) {
	p, f := newTestPlugin(t)
	pg := getPage(t, p, "home", "", "", "zh-TW")
	if got := rowIDs(pg); got != "hero,trending,popular_movies,popular_series,studios,networks" {
		t.Fatalf("rows = %s", got)
	}
	if len(pg.GetFilters()) != 0 || pg.GetSort() != nil {
		t.Errorf("home declares controls: %v %v", pg.GetFilters(), pg.GetSort())
	}
	hero := pg.GetRows()[0]
	if hero.GetStyle() != pluginv1.DiscoverRowStyle_DISCOVER_ROW_STYLE_HERO || len(hero.GetWorks()) != 1 || hero.GetWorks()[0].GetKey() != "movie:603" || hero.GetWorks()[0].GetBackdropUrl() == "" || hero.GetNextPageToken() != "" {
		t.Errorf("hero = %v", hero)
	}
	tr := rowByID(t, pg, "trending")
	if tr.GetTitle() != "熱門趨勢" || tr.GetNextPageToken() != "2" || len(tr.GetWorks()) != 1 || tr.GetWorks()[0].GetKey() != "tv:1396" {
		t.Errorf("trending = %v, want what the hero did not take", tr)
	}
	pm := rowByID(t, pg, "popular_movies")
	if pm.GetMoreRef() != "movies" || pm.GetTitle() != "熱門電影" {
		t.Errorf("popular movies = %v", pm)
	}
	studios := rowByID(t, pg, "studios")
	if studios.GetStyle() != pluginv1.DiscoverRowStyle_DISCOVER_ROW_STYLE_CARDS || len(studios.GetCards()) != len(studioWall) || studios.GetCards()[0].GetRef() != "studio:2" ||
		!strings.HasSuffix(studios.GetCards()[0].GetImageUrl(), "/w500"+studioWall[0].logo) || studios.GetCards()[0].GetImageKind() != pluginv1.DiscoverCardImageKind_DISCOVER_CARD_IMAGE_KIND_LOGO || len(studios.GetWorks()) != 0 {
		t.Errorf("studios = %v", studios)
	}
	if n := rowByID(t, pg, "networks"); n.GetCards()[0].GetRef() != "network:213" {
		t.Errorf("networks = %v", n.GetCards()[0])
	}
	// One Seerr page feeds the hero and the trending row.
	if n := f.count("/api/v1/discover/trending"); n != 1 {
		t.Errorf("trending was asked %d times", n)
	}
	// No list is asked for in the member's language: a page has one language.
	for _, r := range f.reqs {
		if strings.HasPrefix(r.path, "/api/v1/discover/") {
			v, _ := url.ParseQuery(r.query)
			if l := v.Get("language"); l != "" && l != "all" {
				t.Errorf("%s?%s sends a display language", r.path, r.query)
			}
			if strings.HasSuffix(r.path, "trending") && v.Has("language") {
				t.Errorf("trending sends a language: %s", r.query)
			}
		}
	}
	if n := f.count("/api/v1/movie/") + f.count("/api/v1/tv/"); n != 0 {
		t.Errorf("%d title lookups for lists", n)
	}
}

func TestTrendingRowPagesAreWhole(t *testing.T) {
	p, f := newTestPlugin(t)
	pg := getPage(t, p, "home", "trending", "2", "en")
	if r := pg.GetRows()[0]; len(r.GetWorks()) != 2 {
		t.Errorf("page 2 = %v, want both works", r.GetWorks())
	}
	if q := f.last(t); !strings.Contains(q.query, "page=2") {
		t.Errorf("query = %s", q.query)
	}
}

func TestMoviesTabDeclaresControls(t *testing.T) {
	p, _ := newTestPlugin(t)
	pg := getPage(t, p, "movies", "", "", "zh-TW")
	var keys []string
	for _, fl := range pg.GetFilters() {
		keys = append(keys, fl.GetKey())
	}
	if strings.Join(keys, ",") != "genre,period,language,rating" || !pg.GetFilters()[0].GetMultiple() || pg.GetFilters()[2].GetMultiple() {
		t.Errorf("filters = %v", keys)
	}
	if pg.GetFilters()[0].GetLabel() != "類型" || pg.GetFilters()[0].GetChoices()[0].GetLabel() != "动作" {
		t.Errorf("genre filter = %v, want the label in the member's language and Seerr's genre names as they are", pg.GetFilters()[0])
	}
	if pg.GetSort().GetKey() != "sort" || len(pg.GetSort().GetChoices()) != 3 || !pg.GetSort().GetChoices()[0].GetSelected() {
		t.Errorf("sort = %v", pg.GetSort())
	}
	if rowIDs(pg) != "popular_movies" {
		t.Errorf("rows = %s", rowIDs(pg))
	}
	// A row alone carries the same controls, because that is where a filter change is asked.
	alone := getPage(t, p, "movies", "popular_movies", "", "en")
	if len(alone.GetFilters()) != 4 || alone.GetSort() == nil {
		t.Errorf("row alone: %v %v", alone.GetFilters(), alone.GetSort())
	}
}

func TestListingFailsWithoutGenres(t *testing.T) {
	p, _ := newTestPlugin(t)
	p.api.set("http://127.0.0.1:1", "k", "") // Seerr cannot be reached
	if _, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "movies", Languages: langs("en")}); status.Code(err) != codes.Unavailable {
		t.Errorf("err = %v, want UNAVAILABLE", err)
	}
}

func TestListingKeepsTheLastGenresWhenSeerrIsDown(t *testing.T) {
	p, _ := newTestPlugin(t)
	p.genres.ttl = time.Nanosecond
	getPage(t, p, "movies", "", "", "en")
	time.Sleep(5 * time.Millisecond)
	p.api.set("http://127.0.0.1:1", "k", "")
	pg, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "movies", RowId: "popular_movies", Languages: langs("en")})
	if err == nil && pg.GetFilters()[0].GetKey() != "genre" {
		t.Errorf("filters = %v, want the last genre list", pg.GetFilters())
	}
}

func TestFilteredRows(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	cases := []struct {
		name  string
		ref   string
		sel   []*pluginv1.WorkSelection
		want  []string
		avoid []string
	}{
		{"genre period sort", "movies", []*pluginv1.WorkSelection{sel("genre", "878"), sel("period", "1990s"), sel("sort", "newest")},
			[]string{"genre=878", "primaryReleaseDateGte=1990-01-01", "primaryReleaseDateLte=1999-12-31", "sortBy=primary_release_date.desc"}, nil},
		{"newest stops today", "movies", []*pluginv1.WorkSelection{sel("sort", "newest")},
			[]string{"sortBy=primary_release_date.desc", "primaryReleaseDateLte=" + today}, []string{"primaryReleaseDateGte"}},
		{"series dates", "series", []*pluginv1.WorkSelection{sel("period", "2010s"), sel("sort", "top_rated")},
			[]string{"firstAirDateGte=2010-01-01", "firstAirDateLte=2019-12-31", "sortBy=vote_average.desc", "voteCountGte=200"}, []string{"primaryRelease"}},
		{"nothing chosen is Seerr's default order, in every original language", "movies", nil, []string{"language=all"}, []string{"genre", "sortBy", "DateG", "DateL", "voteCount", "voteAverage"}},
		{"several genres are or", "movies", []*pluginv1.WorkSelection{sel("genre", "878", "28", "878", "x")}, []string{"genre=878%7C28"}, nil},
		{"Chinese covers zh and cn", "movies", []*pluginv1.WorkSelection{sel("language", "zh")}, []string{"language=zh%7Ccn"}, nil},
		{"rating with a vote floor", "movies", []*pluginv1.WorkSelection{sel("rating", "7")}, []string{"voteAverageGte=7", "voteCountGte=200"}, nil},
		{"bad values are ignored", "movies", []*pluginv1.WorkSelection{sel("genre", "1;drop"), sel("period", "1850s"), sel("sort", "title"), sel("other", "x"), sel("language", "xx"), sel("rating", "1")},
			nil, []string{"genre", "sortBy", "DateG", "DateL", "voteCount", "other", "voteAverage", "language=zh"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, f := newTestPlugin(t)
			row := map[string]string{"movies": "popular_movies", "series": "popular_series"}[c.ref]
			getPage(t, p, c.ref, row, "2", "en", c.sel...)
			q := ""
			f.mu.Lock()
			for _, r := range f.reqs {
				if strings.HasPrefix(r.path, "/api/v1/discover/") {
					q = r.query
				}
			}
			f.mu.Unlock()
			if !strings.Contains(q, "page=2") {
				t.Errorf("the page is not kept: %s", q)
			}
			for _, w := range c.want {
				if !strings.Contains(q, w) {
					t.Errorf("query %q lacks %q", q, w)
				}
			}
			for _, w := range c.avoid {
				if strings.Contains(q, w) {
					t.Errorf("query %q has %q", q, w)
				}
			}
		})
	}
}

func TestRowTitles(t *testing.T) {
	p, _ := newTestPlugin(t)
	// The Movies and Series tabs are one row named by the tab, whatever the sort.
	for _, s := range []string{"popular", "newest", "top_rated"} {
		if got := getPage(t, p, "series", "popular_series", "", "en", sel("sort", s)).GetRows()[0].GetTitle(); got != "Series" {
			t.Errorf("series tab, sort %s: title %q", s, got)
		}
	}
	if got := getPage(t, p, "movies", "", "", "zh-TW").GetRows()[0].GetTitle(); got != "電影" {
		t.Errorf("movies tab title = %q", got)
	}
	// A genre page names its list by the sort.
	if got := getPage(t, p, "genre:tv:28", "works", "", "en", sel("sort", "top_rated")).GetRows()[0].GetTitle(); got != "Top rated series" {
		t.Errorf("genre page title = %q", got)
	}
}

func TestGenresTabIsAWallOfCards(t *testing.T) {
	p, f := newTestPlugin(t)
	pg := getPage(t, p, "genres", "", "", "zh-CN")
	if rowIDs(pg) != "genres_movies,genres_series" {
		t.Fatalf("rows = %s", rowIDs(pg))
	}
	r := pg.GetRows()[0]
	if r.GetStyle() != pluginv1.DiscoverRowStyle_DISCOVER_ROW_STYLE_CARDS || len(r.GetCards()) != 2 || r.GetTitle() != "电影类型" {
		t.Fatalf("row = %v", r)
	}
	if c := r.GetCards()[0]; c.GetTitle() != "动作" || c.GetRef() != "genre:movie:28" || c.GetImageUrl() != "https://image.tmdb.org/t/p/w780/g28.jpg" {
		t.Errorf("card = %v", c)
	}
	if r.GetCards()[0].GetImageKind() != pluginv1.DiscoverCardImageKind_DISCOVER_CARD_IMAGE_KIND_COVER {
		t.Errorf("a genre card is not a cover")
	}
	if c := r.GetCards()[1]; c.GetImageUrl() != "" {
		t.Errorf("a genre without a backdrop has an image: %v", c)
	}
	getPage(t, p, "genres", "", "", "zh-CN")
	if n := f.count("/api/v1/discover/genreslider/movie"); n != 1 {
		t.Errorf("the slider was asked %d times, want 1 (cached)", n)
	}
}

func TestGenrePage(t *testing.T) {
	p, f := newTestPlugin(t)
	pg := getPage(t, p, "genre:tv:28", "", "", "en", sel("sort", "newest"))
	if h := pg.GetHeader(); h.GetTitle() != "动作" || h.GetSubtitle() != "Series" {
		t.Errorf("header = %v", h)
	}
	for _, fl := range pg.GetFilters() {
		if fl.GetKey() == "genre" {
			t.Errorf("a genre page offers the genre filter")
		}
	}
	q := f.last(t).query
	if !strings.Contains(q, "genre=28") || !strings.Contains(q, "sortBy=first_air_date.desc") {
		t.Errorf("query = %s", q)
	}
	if _, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "genre:movie:99999"}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown genre: %v", err)
	}
}

func TestStudioAndNetworkPages(t *testing.T) {
	p, f := newTestPlugin(t)
	pg := getPage(t, p, "studio:79", "", "", "en")
	h := pg.GetHeader()
	if h.GetTitle() != "Village Roadshow Pictures" || h.GetOverview() != "A studio" || !strings.HasSuffix(h.GetImageUrl(), "/s79.png") || len(h.GetFacts()) != 2 || h.GetFacts()[0].GetValue() != "Burbank, CA" {
		t.Errorf("header = %v", h)
	}
	var q string
	for _, r := range f.reqs {
		if r.path == "/api/v1/discover/movies" {
			q = r.query
		}
	}
	if !strings.Contains(q, "studio=79") || strings.Contains(q, "genre") {
		t.Errorf("studio query = %s", q)
	}
	pg = getPage(t, p, "network:174", "", "", "en")
	if pg.GetHeader().GetTitle() != "AMC" || len(pg.GetHeader().GetFacts()) != 1 {
		t.Errorf("network header = %v", pg.GetHeader())
	}
	if q := f.last(t); !strings.Contains(q.query+q.path, "network=174") && f.count("/api/v1/discover/tv") == 0 {
		t.Errorf("network list not asked: %v", q)
	}
	if _, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "studio:5"}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown studio: %v", err)
	}
}

func TestPersonPage(t *testing.T) {
	p, f := newTestPlugin(t)
	pg := getPage(t, p, "person:6384", "", "", "en")
	h := pg.GetHeader()
	if h.GetTitle() != "Keanu" || h.GetOverview() != "Bio" || !strings.HasSuffix(h.GetImageUrl(), "w185/k.jpg") {
		t.Errorf("header = %v", h)
	}
	if len(h.GetFacts()) != 2 || h.GetFacts()[0].GetLabel() != "Born" || h.GetFacts()[0].GetValue() != "1964-09-02" {
		t.Errorf("facts = %v, want born and place only (deathday is null)", h.GetFacts())
	}
	r := pg.GetRows()[0]
	var got []string
	for _, w := range r.GetWorks() {
		got = append(got, w.GetKey()+"/"+w.GetCaption())
	}
	// New first, then 2015, then 1991, then the undated one; the talk-show appearance and the person entry are gone.
	if want := "movie:13/Neo,tv:14/Himself,movie:10/Ted,movie:11/Nobody"; strings.Join(got, ",") != want {
		t.Errorf("works = %v, want %s", got, want)
	}
	// Seerr's list is asked in Seerr's language, and the badge status is kept from it.
	for _, rq := range f.reqs {
		if strings.HasSuffix(rq.path, "combined_credits") && rq.query != "" {
			t.Errorf("credits asked with %q", rq.query)
		}
	}
	if st, ok := p.states.get("movie", 13); !ok || st.status != 5 {
		t.Errorf("status of the first work = %v %v", st, ok)
	}
}

func TestPersonWorksPageByOffset(t *testing.T) {
	p, f := newTestPlugin(t)
	var rs []string
	for i := 1; i <= 45; i++ {
		rs = append(rs, `{"id":`+itoa(1000+i)+`,"mediaType":"movie","title":"T","releaseDate":"2000-01-`+("0"+itoa(i%9+1))+`","character":"c"}`)
	}
	f.credits = `{"cast":[` + strings.Join(rs, ",") + `],"crew":[]}`
	first := getPage(t, p, "person:6384", "credits", "", "en").GetRows()[0]
	if len(first.GetWorks()) != 20 || first.GetNextPageToken() != "2" {
		t.Fatalf("first page = %d works, token %q", len(first.GetWorks()), first.GetNextPageToken())
	}
	last := getPage(t, p, "person:6384", "credits", "3", "en").GetRows()[0]
	if len(last.GetWorks()) != 5 || last.GetNextPageToken() != "" {
		t.Errorf("last page = %d works, token %q", len(last.GetWorks()), last.GetNextPageToken())
	}
	if n := f.count("/api/v1/person/6384/combined_credits"); n != 1 {
		t.Errorf("credits were asked %d times for three pages, want 1", n)
	}
	// A row alone has no header and costs no /person call.
	if n := f.count("/api/v1/person/6384"); n != 3 && f.count("/api/v1/person/6384?") != 0 {
		t.Errorf("person asked %d times", n)
	}
	beyond := getPage(t, p, "person:6384", "credits", "9", "en").GetRows()[0]
	if len(beyond.GetWorks()) != 0 || beyond.GetNextPageToken() != "" {
		t.Errorf("past the end: %v", beyond)
	}
}

func TestPersonWithOnlyCrewWork(t *testing.T) {
	p, _ := newTestPlugin(t)
	r := getPage(t, p, "person:7", "", "", "en").GetRows()[0]
	if len(r.GetWorks()) != 2 {
		t.Fatalf("works = %v", r.GetWorks())
	}
	if w := r.GetWorks()[0]; w.GetKey() != "movie:603" || w.GetCaption() != "Producer, Director" {
		t.Errorf("first = %s %q, want one entry with the jobs joined", w.GetKey(), w.GetCaption())
	}
	if r.GetWorks()[1].GetKey() != "tv:1396" { // no date: last
		t.Errorf("order = %v", r.GetWorks())
	}
}

func TestUnknownPersonIsNotFound(t *testing.T) {
	p, _ := newTestPlugin(t)
	if _, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "person:424242"}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown person: %v", err)
	}
	if _, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "person:424242", RowId: "credits"}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown person, row: %v", err)
	}
}

func TestRecommendationsPage(t *testing.T) {
	p, f := newTestPlugin(t)
	pg := getPage(t, p, "rec:movie:603", "", "", "en")
	r := pg.GetRows()[0]
	if r.GetId() != "recommended" || len(r.GetWorks()) != 1 || r.GetNextPageToken() != "2" {
		t.Errorf("row = %v", r)
	}
	if q := f.last(t); q.path != "/api/v1/movie/603/recommendations" || strings.Contains(q.query, "language") {
		t.Errorf("request = %+v", q)
	}
	getPage(t, p, "rec:tv:1396", "recommended", "2", "en")
	if q := f.last(t); q.path != "/api/v1/tv/1396/recommendations" || !strings.Contains(q.query, "page=2") {
		t.Errorf("request = %+v", q)
	}
}

func TestListPagesAreCachedForAFewMinutes(t *testing.T) {
	p, f := newTestPlugin(t)
	for range 3 {
		getPage(t, p, "movies", "popular_movies", "", "en", sel("genre", "28"))
	}
	if n := f.count("/api/v1/discover/movies"); n != 1 {
		t.Errorf("Seerr was asked %d times for the same page, want 1", n)
	}
	getPage(t, p, "movies", "popular_movies", "", "en", sel("genre", "35"))
	if n := f.count("/api/v1/discover/movies"); n != 2 {
		t.Errorf("another filter must be another page: %d requests", n)
	}
	p.pages.ttl = time.Nanosecond
	time.Sleep(5 * time.Millisecond)
	getPage(t, p, "movies", "popular_movies", "", "en", sel("genre", "35"))
	if n := f.count("/api/v1/discover/movies"); n != 3 {
		t.Errorf("an old page must be asked again: %d requests", n)
	}
}

func TestGetWorkDetails(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = `{"id":603,"title":"Movie A","tagline":"Free your mind","genres":[{"id":28,"name":"Action"},{"id":878,"name":"Sci-Fi"}],
	 "productionCompanies":[{"id":79,"name":"Village"},{"id":0,"name":"No ID"},{"id":79,"name":"Village"}],
	 "credits":{"cast":[{"id":1,"name":"Keanu","character":"Neo","profilePath":"/k.jpg"},{"id":2,"name":"","character":"x"},{"id":3,"name":"Carrie","character":"Trinity"}],
	  "crew":[{"id":9,"name":"Lana","job":"Director","profilePath":"/l.jpg"},{"id":9,"name":"Lana","job":"Writer"},{"id":8,"name":"Hans","job":"Composer"}]},
	 "relatedVideos":[{"site":"YouTube","type":"Teaser","name":"Teaser","url":"https://www.youtube.com/watch?v=a"},
	  {"site":"YouTube","type":"Trailer","name":"Official Trailer","url":"https://www.youtube.com/watch?v=b"},
	  {"site":"x","type":"Trailer","name":"Plain","url":"http://insecure.example/c"},
	  {"site":"YouTube","type":"Trailer","name":"","url":"https://www.youtube.com/watch?v=d"},
	  {"site":"YouTube","type":"Trailer","name":"Third","url":"https://www.youtube.com/watch?v=e"}]}`
	w, err := p.GetWork(ctx, &pluginv1.GetWorkRequest{Key: "movie:603", Languages: langs("zh-TW")})
	if err != nil {
		t.Fatal(err)
	}
	if w.GetTagline() != "Free your mind" || len(w.GetGenres()) != 2 || w.GetGenres()[0].GetRef() != "genre:movie:28" {
		t.Errorf("tagline/genres = %q %v", w.GetTagline(), w.GetGenres())
	}
	if len(w.GetCompanies()) != 2 || w.GetCompanies()[0].GetRef() != "studio:79" || w.GetCompanies()[1].GetRef() != "" {
		t.Errorf("companies = %v, want deduped, and no ref without an ID", w.GetCompanies())
	}
	var cr []string
	for _, c := range w.GetCredits() {
		cr = append(cr, c.GetName()+"/"+c.GetKind().String()[len("WORK_CREDIT_KIND_"):]+"/"+c.GetRole()+"/"+c.GetRef())
	}
	if want := "Lana/DIRECTOR//person:9,Keanu/ACTOR/Neo/person:1,Carrie/ACTOR/Trinity/person:3"; strings.Join(cr, ",") != want {
		t.Errorf("credits = %v, want %s", cr, want)
	}
	if w.GetCredits()[0].GetImageUrl() != "https://image.tmdb.org/t/p/w185/l.jpg" {
		t.Errorf("image = %q", w.GetCredits()[0].GetImageUrl())
	}
	if w.GetRelatedRef() != "rec:movie:603" {
		t.Errorf("related = %q", w.GetRelatedRef())
	}
	var links []string
	for _, l := range w.GetExternalLinks() {
		links = append(links, l.GetLabel()+"="+l.GetUrl())
	}
	if want := "Official Trailer=https://www.youtube.com/watch?v=b,預告片=https://www.youtube.com/watch?v=d"; strings.Join(links, ",") != want {
		t.Errorf("links = %v, want https trailers only, two at most", links)
	}
	if _, ok := parseRef(w.GetRelatedRef()); !ok {
		t.Errorf("related ref does not parse")
	}
}

func TestGetWorkSeriesDetails(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["tv:1396"] = `{"id":1396,"name":"Series B","networks":[{"id":174,"name":"AMC"}],"productionCompanies":[{"id":11073,"name":"Sony TV"}],
	 "createdBy":[{"id":66633,"name":"Vince","profile_path":"/v.jpg"}],"credits":{"cast":[{"id":17419,"name":"Bryan","character":"Walt"}],"crew":[]}}`
	w, err := p.GetWork(ctx, &pluginv1.GetWorkRequest{Key: "tv:1396", Languages: langs("en")})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.GetCompanies()) != 2 || w.GetCompanies()[0].GetText() != "AMC" || w.GetCompanies()[0].GetRef() != "network:174" || w.GetCompanies()[1].GetRef() != "" {
		t.Errorf("companies = %v, want the network first with a ref and the studio as text", w.GetCompanies())
	}
	if c := w.GetCredits()[0]; c.GetKind() != pluginv1.WorkCreditKind_WORK_CREDIT_KIND_CREATOR || c.GetName() != "Vince" || c.GetImageUrl() != "https://image.tmdb.org/t/p/w185/v.jpg" {
		t.Errorf("creator = %v", c)
	}
	if w.GetRelatedRef() != "rec:tv:1396" {
		t.Errorf("related = %q", w.GetRelatedRef())
	}
}

func TestCreditsAreCapped(t *testing.T) {
	var cast []person
	for i := 1; i <= 80; i++ {
		cast = append(cast, person{ID: i, Name: "P" + itoa(i)})
	}
	r := result{}
	r.Credits.Cast = cast
	r.Credits.Crew = []person{{ID: 500, Name: "D", Job: "Director"}}
	got := credits(r, kindMovie)
	if len(got) != 30 || got[0].GetName() != "D" || got[1].GetName() != "P1" {
		t.Errorf("%d credits, first %q %q", len(got), got[0].GetName(), got[1].GetName())
	}
}

func TestARowAskedAloneCarriesTheHeader(t *testing.T) {
	p, _ := newTestPlugin(t)
	for ref, row := range map[string]string{"person:6384": "credits", "studio:79": "works", "network:174": "works", "genre:movie:28": "works"} {
		pg := getPage(t, p, ref, row, "", "en")
		if pg.GetHeader().GetTitle() == "" || len(pg.GetRows()) != 1 {
			t.Errorf("%s/%s: header %q, %d rows", ref, row, pg.GetHeader().GetTitle(), len(pg.GetRows()))
		}
		if pg.GetHeader().GetBackdropUrl() != "" {
			t.Errorf("%s: header has a backdrop", ref)
		}
	}
}

// A title whose release date is later than today (UTC) is marked "not out yet"; a missing date is not guessed.
func TestUnreleasedMarks(t *testing.T) {
	old := today
	today = func() string { return "2026-11-05" }
	t.Cleanup(func() { today = old })
	p, f := newTestPlugin(t)
	f.list = `{"page":1,"totalPages":1,"results":[
	 {"id":1,"mediaType":"movie","title":"Future","releaseDate":"2027-03-05"},
	 {"id":2,"mediaType":"movie","title":"Today","releaseDate":"2026-11-05"},
	 {"id":3,"mediaType":"movie","title":"Past","releaseDate":"2026-11-04"},
	 {"id":4,"mediaType":"movie","title":"Undated","releaseDate":""},
	 {"id":5,"mediaType":"tv","name":"Coming","firstAirDate":"2026-12-01"},
	 {"id":6,"mediaType":"tv","name":"Running","firstAirDate":"2020-01-01"},
	 {"id":7,"mediaType":"movie","title":"Odd","releaseDate":"2027"}]}`
	caps := func(lang string) []string {
		var out []string
		for _, w := range getPage(t, p, "movies", "popular_movies", "", lang).GetRows()[0].GetWorks() {
			out = append(out, w.GetTitle()+"="+w.GetCaption())
		}
		return out
	}
	if got, want := strings.Join(caps("zh-TW"), ","), "Future=未上映,Today=,Past=,Undated=,Odd="; got != want {
		t.Errorf("captions = %s, want %s", got, want)
	}
	if got := caps("en")[0]; got != "Future=Not out yet" {
		t.Errorf("en = %q", got)
	}
	var tv []string
	for _, w := range getPage(t, p, "series", "popular_series", "", "zh-CN").GetRows()[0].GetWorks() {
		tv = append(tv, w.GetTitle()+"="+w.GetCaption())
	}
	if want := "Coming=未上映,Running="; strings.Join(tv, ",") != want {
		t.Errorf("series = %v, want %s", tv, want)
	}
	// Search lists carry it too.
	rs := []result{{ID: 1, MediaType: kindMovie, Title: "Future", ReleaseDate: "2027-03-05"}}
	if w := works(rs, "", messages.For("zh-CN")); w[0].GetCaption() != "未上映" {
		t.Errorf("works = %v", w)
	}
}

func TestUnreleasedOnAPersonsPage(t *testing.T) {
	old := today
	today = func() string { return "2026-11-05" }
	t.Cleanup(func() { today = old })
	p, f := newTestPlugin(t)
	f.credits = `{"cast":[
	 {"id":1,"mediaType":"movie","title":"Soon","releaseDate":"2027-01-01","character":"Neo"},
	 {"id":2,"mediaType":"movie","title":"SoonNoRole","releaseDate":"2027-02-01"},
	 {"id":3,"mediaType":"movie","title":"Out","releaseDate":"2020-01-01","character":"Ted"}],"crew":[]}`
	var got []string
	for _, w := range getPage(t, p, "person:6384", "credits", "", "zh-TW").GetRows()[0].GetWorks() {
		got = append(got, w.GetTitle()+"="+w.GetCaption())
	}
	if want := "SoonNoRole=未上映,Soon=Neo · 未上映,Out=Ted"; strings.Join(got, ",") != want {
		t.Errorf("captions = %v, want %s", got, want)
	}
}

func TestUnreleasedOnTheDetailPage(t *testing.T) {
	old := today
	today = func() string { return "2026-11-05" }
	t.Cleanup(func() { today = old })
	cases := []struct{ name, key, body, lang, want string }{
		{"future date", "movie:1", `{"id":1,"title":"A","releaseDate":"2027-03-05","status":"Post Production"}`, "zh-TW", "未上映 · 2027-03-05 上映"},
		{"future date, English", "movie:1", `{"id":1,"title":"A","releaseDate":"2027-03-05"}`, "en", "Not out yet · Out 2027-03-05"},
		{"released", "movie:2", `{"id":2,"title":"A","releaseDate":"2020-03-05","status":"Released"}`, "en", ""},
		{"in production without a date", "movie:3", `{"id":3,"title":"A","status":"In Production"}`, "zh-CN", "未上映"},
		{"planned series without a date", "tv:4", `{"id":4,"name":"A","status":"Planned"}`, "en", "Not out yet"},
		{"no date and nothing said", "movie:5", `{"id":5,"title":"A","status":"Released"}`, "en", ""},
		{"series on the air that Seerr calls in production", "tv:6", `{"id":6,"name":"A","firstAirDate":"2020-01-01","status":"In Production"}`, "en", ""},
		{"future series", "tv:7", `{"id":7,"name":"A","firstAirDate":"2026-12-24"}`, "en", "Not out yet · Out 2026-12-24"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, f := newTestPlugin(t)
			f.details[c.key] = c.body
			w, err := p.GetWork(ctx, &pluginv1.GetWorkRequest{Key: c.key, Languages: langs(c.lang)})
			if err != nil || w.GetCaption() != c.want {
				t.Errorf("caption = %q (%v), want %q", w.GetCaption(), err, c.want)
			}
		})
	}
}
