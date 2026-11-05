package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func sel(key string, values ...string) *pluginv1.WorkSelection {
	return &pluginv1.WorkSelection{Key: key, Values: values}
}

func TestDescribeDiscoverSource(t *testing.T) {
	p, _ := newTestPlugin(t)
	info, err := p.DescribeDiscoverSource(ctx, &pluginv1.DescribeDiscoverSourceRequest{Languages: langs("zh-TW")})
	if err != nil {
		t.Fatal(err)
	}
	if len(info.GetKinds()) != 2 || info.GetKinds()[0].GetType() != pluginv1.ItemType_ITEM_TYPE_MOVIE || info.GetKinds()[1].GetType() != pluginv1.ItemType_ITEM_TYPE_SERIES {
		t.Fatalf("kinds = %v", info.GetKinds())
	}
	for _, k := range info.GetKinds() {
		keys := map[string]bool{}
		for _, f := range k.GetFilters() {
			if keys[f.GetKey()] || f.GetRequired() {
				t.Errorf("filter %q repeated or required", f.GetKey())
			}
			keys[f.GetKey()] = true
		}
		if k.GetSort() == nil || keys[k.GetSort().GetKey()] {
			t.Errorf("sort = %v", k.GetSort())
		}
		if len(k.GetFilters()) != 4 || k.GetFilters()[0].GetKey() != "genre" || k.GetFilters()[1].GetKey() != "period" || k.GetFilters()[2].GetKey() != "language" || k.GetFilters()[3].GetKey() != "rating" || !k.GetFilters()[0].GetMultiple() || k.GetFilters()[2].GetMultiple() || k.GetFilters()[0].GetLabel() != "類型" {
			t.Errorf("filters = %v", k.GetFilters())
		}
	}
	genres := info.GetKinds()[0].GetFilters()[0].GetChoices()
	if len(genres) != 3 || genres[0].GetValue() != "28" || genres[0].GetLabel() != "動作" || genres[2].GetLabel() != "Comedy" {
		t.Errorf("zh-TW genres = %v, want Seerr's Simplified names turned Traditional", genres)
	}
	periods := info.GetKinds()[0].GetFilters()[1].GetChoices()
	if len(periods) != 6 || periods[0].GetLabel() != "2020 年代" || periods[5].GetLabel() != "1980 年以前" {
		t.Errorf("periods = %v", periods)
	}
	sort := info.GetKinds()[0].GetSort().GetChoices()
	if len(sort) != 4 || !sort[0].GetSelected() || sort[1].GetSelected() {
		t.Errorf("sort = %v, want the first selected", sort)
	}
	cn, _ := p.DescribeDiscoverSource(ctx, &pluginv1.DescribeDiscoverSourceRequest{Languages: langs("zh-CN")})
	if l := cn.GetKinds()[0].GetFilters()[0].GetChoices()[0].GetLabel(); l != "动作" {
		t.Errorf("zh-CN genre = %q, want it as Seerr gives it", l)
	}
}

func TestDescribeWithoutGenres(t *testing.T) {
	p, _ := newTestPlugin(t)
	p.api.set("http://127.0.0.1:1", "k", "") // Seerr cannot be reached
	info, err := p.DescribeDiscoverSource(ctx, &pluginv1.DescribeDiscoverSourceRequest{Languages: langs("en")})
	if err != nil || len(info.GetKinds()) != 2 {
		t.Fatalf("info = %v (%v), want the source to stay up", info, err)
	}
	for _, k := range info.GetKinds() {
		if len(k.GetFilters()) != 3 || k.GetFilters()[0].GetKey() != "period" {
			t.Errorf("filters = %v, want the period filter only", k.GetFilters())
		}
	}
}

func TestDescribeKeepsTheLastGenresWhenSeerrIsDown(t *testing.T) {
	p, _ := newTestPlugin(t)
	p.genres.ttl = time.Nanosecond
	if _, err := p.DescribeDiscoverSource(ctx, &pluginv1.DescribeDiscoverSourceRequest{Languages: langs("en")}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	p.api.set("http://127.0.0.1:1", "k", "")
	info, err := p.DescribeDiscoverSource(ctx, &pluginv1.DescribeDiscoverSourceRequest{Languages: langs("en")})
	if err != nil || info.GetKinds()[0].GetFilters()[0].GetKey() != "genre" {
		t.Errorf("info = %v (%v), want the last genre list", info, err)
	}
}

func TestFilteredRows(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	cases := []struct {
		name  string
		row   string
		sel   []*pluginv1.WorkSelection
		want  []string
		avoid []string
	}{
		{"genre period sort", "popular_movies", []*pluginv1.WorkSelection{sel("genre", "878"), sel("period", "1990s"), sel("sort", "newest")},
			[]string{"genre=878", "primaryReleaseDateGte=1990-01-01", "primaryReleaseDateLte=1999-12-31", "sortBy=primary_release_date.desc"}, nil},
		{"newest stops today", "popular_movies", []*pluginv1.WorkSelection{sel("sort", "newest")},
			[]string{"sortBy=primary_release_date.desc", "primaryReleaseDateLte=" + today}, []string{"primaryReleaseDateGte"}},
		{"newest in a period that is not over", "popular_movies", []*pluginv1.WorkSelection{sel("period", "2020s"), sel("sort", "newest")},
			[]string{"primaryReleaseDateGte=2020-01-01", "primaryReleaseDateLte=" + today}, nil},
		{"series dates", "popular_series", []*pluginv1.WorkSelection{sel("period", "2010s"), sel("sort", "top_rated")},
			[]string{"firstAirDateGte=2010-01-01", "firstAirDateLte=2019-12-31", "sortBy=vote_average.desc", "voteCountGte=200"}, []string{"primaryRelease"}},
		{"older", "popular_movies", []*pluginv1.WorkSelection{sel("period", "older")},
			[]string{"primaryReleaseDateLte=1979-12-31"}, []string{"primaryReleaseDateGte"}},
		{"nothing chosen is Seerr's default order, in every original language", "popular_movies", nil, []string{"language=all"}, []string{"genre", "sortBy", "DateG", "DateL", "voteCount", "voteAverage"}},
		{"several genres are or", "popular_movies", []*pluginv1.WorkSelection{sel("genre", "878", "28", "878", "x")}, []string{"genre=878%7C28"}, nil},
		{"Chinese covers zh and cn", "popular_movies", []*pluginv1.WorkSelection{sel("language", "zh")}, []string{"language=zh%7Ccn"}, nil},
		{"Japanese", "popular_series", []*pluginv1.WorkSelection{sel("language", "ja")}, []string{"language=ja"}, nil},
		{"rating with a vote floor", "popular_movies", []*pluginv1.WorkSelection{sel("rating", "7")}, []string{"voteAverageGte=7", "voteCountGte=200"}, nil},
		{"rating on series", "popular_series", []*pluginv1.WorkSelection{sel("rating", "8"), sel("sort", "top_rated")}, []string{"voteAverageGte=8", "voteCountGte=200", "sortBy=vote_average.desc"}, nil},
		{"title sort", "popular_movies", []*pluginv1.WorkSelection{sel("sort", "title")}, []string{"sortBy=original_title.asc", "voteCountGte=20"}, nil},
		{"title sort series", "popular_series", []*pluginv1.WorkSelection{sel("sort", "title")}, []string{"sortBy=original_name.asc", "voteCountGte=10"}, nil},
		{"all conditions", "popular_movies", []*pluginv1.WorkSelection{sel("genre", "16", "10751"), sel("period", "2010s"), sel("language", "ko"), sel("rating", "6"), sel("sort", "newest")},
			[]string{"genre=16%7C10751", "language=ko", "voteAverageGte=6", "primaryReleaseDateGte=2010-01-01", "sortBy=primary_release_date.desc"}, nil},
		{"bad values are ignored", "popular_movies", []*pluginv1.WorkSelection{sel("genre", "1;drop"), sel("period", "1850s"), sel("sort", "bogus"), sel("other", "x"), sel("language", "xx"), sel("rating", "1")},
			nil, []string{"genre", "sortBy", "DateG", "DateL", "voteCount", "other", "voteAverage", "language=zh"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, f := newTestPlugin(t)
			f.details["movie:603"] = movieWith("")
			f.details["tv:1396"] = tvWith("")
			ty := pluginv1.ItemType_ITEM_TYPE_MOVIE
			if c.row == "popular_series" {
				ty = pluginv1.ItemType_ITEM_TYPE_SERIES
			}
			resp, err := p.ListDiscoverRows(ctx, &pluginv1.ListDiscoverRowsRequest{Type: ty, RowId: c.row, Selections: c.sel, Languages: langs("en"), PageToken: "2"})
			if err != nil || len(resp.GetRows()) != 1 {
				t.Fatalf("rows: %v %v", resp, err)
			}
			if got := resp.GetRows()[0].GetFilterKeys(); strings.Join(got, ",") != "genre,period,language,rating,sort" {
				t.Errorf("filter_keys = %v", got)
			}
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

func TestRowTitlesFollowTheSort(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith("")
	resp, err := p.ListDiscoverRows(ctx, &pluginv1.ListDiscoverRowsRequest{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, RowId: "popular_movies",
		Selections: []*pluginv1.WorkSelection{sel("sort", "top_rated")}, Languages: langs("en")})
	if err != nil || resp.GetRows()[0].GetTitle() != "Top rated movies" {
		t.Errorf("title = %v (%v)", resp.GetRows()[0].GetTitle(), err)
	}
}

func TestTrendingCannotBeFiltered(t *testing.T) {
	p, _ := newTestPlugin(t)
	resp, err := p.ListDiscoverRows(ctx, &pluginv1.ListDiscoverRowsRequest{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, Languages: langs("en")})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resp.GetRows() {
		if (r.GetId() == "trending") != (len(r.GetFilterKeys()) == 0) {
			t.Errorf("row %s filter_keys = %v: only trending has none", r.GetId(), r.GetFilterKeys())
		}
	}
	// A selection that arrives for trending is ignored.
	one, err := p.ListDiscoverRows(ctx, &pluginv1.ListDiscoverRowsRequest{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, RowId: "trending", Selections: []*pluginv1.WorkSelection{sel("genre", "28")}})
	if err != nil || len(one.GetRows()) != 1 {
		t.Errorf("trending with a selection: %v %v", one, err)
	}
}

func TestOnlyDeclaredTypesAreAsked(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith("")
	resp, err := p.ListDiscoverRows(ctx, &pluginv1.ListDiscoverRowsRequest{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, Languages: langs("en")})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resp.GetRows() {
		for _, w := range r.GetWorks() {
			if w.GetType() != pluginv1.ItemType_ITEM_TYPE_MOVIE {
				t.Errorf("row %s has %s", r.GetId(), w.GetKey())
			}
		}
	}
}

func TestMemberWorks(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = `{"id":603,"title":"Movie A","releaseDate":"1999-03-30","posterPath":"/p.jpg","originalTitle":"Movie A orig"}`
	f.details["tv:1396"] = `{"id":1396,"name":"Series B","firstAirDate":"2008-01-20"}`
	f.requests = `{"pageInfo":{"pages":2,"pageSize":20,"results":25,"page":1},"results":[
	 {"id":9,"type":"movie","status":2,"updatedAt":"2026-11-04T10:00:00.000Z","requestedBy":{"id":5},
	  "media":{"tmdbId":603,"status":3,"downloadStatus":[{"size":1000,"sizeLeft":250},{"size":1000,"sizeLeft":750}]}},
	 {"id":8,"type":"tv","status":3,"updatedAt":"not a time","requestedBy":{"id":5},"media":{"tmdbId":1396,"status":1}},
	 {"id":7,"type":"movie","status":1,"requestedBy":{"id":5},"media":{"tmdbId":777,"status":2}}]}`
	resp, err := p.ListMemberWorks(ctx, &pluginv1.ListMemberWorksRequest{Member: mia, Languages: langs("en")})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetWorks()) != 3 || resp.GetNextPageToken() != "20" {
		t.Fatalf("works = %d token %q", len(resp.GetWorks()), resp.GetNextPageToken())
	}
	a, b, c := resp.GetWorks()[0], resp.GetWorks()[1], resp.GetWorks()[2]
	if a.GetWork().GetTitle() != "Movie A" || a.GetWork().GetKey() != "movie:603" || a.GetWork().GetYear() != 1999 || a.GetWork().GetProviderIds()["tmdb"] != "603" ||
		a.GetStatus() != pluginv1.WorkStatus_WORK_STATUS_PROCESSING || a.GetStatusText() != "Processing" || a.GetUpdatedAt() != "2026-11-04T10:00:00.000Z" {
		t.Errorf("first = %v", a)
	}
	if a.ProgressPercent == nil || *a.ProgressPercent != 50 {
		t.Errorf("progress = %v, want 50 (1000 of 2000 bytes done)", a.ProgressPercent)
	}
	if b.GetWork().GetTitle() != "Series B" || b.GetWork().GetType() != pluginv1.ItemType_ITEM_TYPE_SERIES || b.GetUpdatedAt() != "" ||
		b.GetStatus() != pluginv1.WorkStatus_WORK_STATUS_DECLINED || b.ProgressPercent != nil {
		t.Errorf("second = %v", b)
	}
	if c.GetWork().GetTitle() != "Untitled" || c.GetWork().GetKey() != "movie:777" || c.GetStatus() != pluginv1.WorkStatus_WORK_STATUS_PENDING {
		t.Errorf("third = %v", c)
	}
	var asked rec
	f.mu.Lock()
	for _, r := range f.reqs {
		if r.path == "/api/v1/request" && r.method == "GET" {
			asked = r
		}
	}
	f.mu.Unlock()
	if asked.user != "5" || !strings.Contains(asked.query, "sort=modified") || !strings.Contains(asked.query, "take=20") || strings.Contains(asked.query, "requestedBy") {
		t.Errorf("request to Seerr = %+v, want the member's Seerr user and no requestedBy", asked)
	}
	next, err := p.ListMemberWorks(ctx, &pluginv1.ListMemberWorksRequest{Member: mia, PageToken: "20"})
	if err != nil || !strings.Contains(lastGet(f, "/api/v1/request").query, "skip=20") {
		t.Errorf("next page: %v %v", next, err)
	}
}

func lastGet(f *fakeSeerr, path string) rec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out rec
	for _, r := range f.reqs {
		if r.path == path && r.method == "GET" {
			out = r
		}
	}
	return out
}

func TestMemberWorksEdges(t *testing.T) {
	p, f := newTestPlugin(t)
	resp, err := p.ListMemberWorks(ctx, &pluginv1.ListMemberWorksRequest{Member: &pluginv1.WorkMember{Id: "x", Name: "Zed"}})
	if err != nil || len(resp.GetWorks()) != 0 {
		t.Errorf("a member Seerr does not know: %v %v, want an empty list", resp, err)
	}
	if n := f.count("/api/v1/request"); n != 0 {
		t.Errorf("Seerr was asked for requests without a user: %d", n)
	}
	for _, tok := range []string{"x", "-1", "999999999"} {
		if _, err := p.ListMemberWorks(ctx, &pluginv1.ListMemberWorksRequest{Member: mia, PageToken: tok}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("token %q: %v, want INVALID_ARGUMENT", tok, err)
		}
	}
	empty, err := p.ListMemberWorks(ctx, &pluginv1.ListMemberWorksRequest{Member: mia})
	if err != nil || len(empty.GetWorks()) != 0 || empty.GetNextPageToken() != "" {
		t.Errorf("no requests: %v %v", empty, err)
	}
}

func TestMemberWorksOnePageWithinTheBudget(t *testing.T) {
	p, f := newTestPlugin(t)
	f.lookupDelay = 600 * time.Millisecond
	var rs []string
	for i := 1; i <= 20; i++ {
		id := 1000 + i
		f.details["movie:"+itoa(id)] = `{"id":` + itoa(id) + `,"title":"T` + itoa(id) + `"}`
		rs = append(rs, `{"id":`+itoa(i)+`,"type":"movie","status":1,"updatedAt":"2026-11-04T10:00:00.000Z","media":{"tmdbId":`+itoa(id)+`,"status":2}}`)
	}
	f.requests = `{"pageInfo":{"pages":1,"pageSize":20,"results":20,"page":1},"results":[` + strings.Join(rs, ",") + `]}`
	start := time.Now()
	resp, err := p.ListMemberWorks(ctx, &pluginv1.ListMemberWorksRequest{Member: mia, Languages: langs("en")})
	if err != nil || len(resp.GetWorks()) != 20 {
		t.Fatalf("works = %v (%v)", len(resp.GetWorks()), err)
	}
	// 20 lookups of 0.6 s, eight at a time: three rounds, about 1.8 s; far inside the host's 15 seconds.
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("a page of 20 took %v", d)
	}
	if resp.GetWorks()[19].GetWork().GetTitle() != "T1020" {
		t.Errorf("last title = %q", resp.GetWorks()[19].GetWork().GetTitle())
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestListPagesAreCachedForAFewMinutes(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith("")
	req := &pluginv1.ListDiscoverRowsRequest{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, RowId: "popular_movies", Languages: langs("en"),
		Selections: []*pluginv1.WorkSelection{sel("genre", "28")}}
	for range 3 {
		if _, err := p.ListDiscoverRows(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count("/api/v1/discover/movies"); n != 1 {
		t.Errorf("Seerr was asked %d times for the same page, want 1", n)
	}
	req.Selections = []*pluginv1.WorkSelection{sel("genre", "35")}
	if _, err := p.ListDiscoverRows(ctx, req); err != nil {
		t.Fatal(err)
	}
	if n := f.count("/api/v1/discover/movies"); n != 2 {
		t.Errorf("another filter must be another page: %d requests", n)
	}
	p.pages.ttl = time.Nanosecond
	time.Sleep(5 * time.Millisecond)
	if _, err := p.ListDiscoverRows(ctx, req); err != nil {
		t.Fatal(err)
	}
	if n := f.count("/api/v1/discover/movies"); n != 3 {
		t.Errorf("an old page must be asked again: %d requests", n)
	}
}
