package main

import (
	"context"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ctx = context.Background()

func langs(l string) *pluginv1.Languages { return &pluginv1.Languages{Language: l} }

func TestConfigure(t *testing.T) {
	p := newPlugin()
	cases := []struct {
		name         string
		url, key     string
		wantURLError bool
		wantKeyError bool
		wantBase     string
	}{
		{"plain", "http://10.0.0.2:5055", "k", false, false, "http://10.0.0.2:5055"},
		{"trailing slash and api path", " https://seerr.lan/api/v1/ ", "k", false, false, "https://seerr.lan"},
		{"sub path kept", "https://host.lan/seerr/", "k", false, false, "https://host.lan/seerr"},
		{"no scheme", "10.0.0.2:5055", "k", true, false, ""},
		{"ftp", "ftp://10.0.0.2", "k", true, false, ""},
		{"user info", "http://a:b@10.0.0.2", "k", true, false, ""},
		{"query", "http://10.0.0.2/?x=1", "k", true, false, ""},
		{"empty key", "http://10.0.0.2", "  ", false, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := p.Configure(ctx, &pluginv1.ConfigureRequest{Values: map[string]string{keyBaseURL: c.url, keyAPIKey: c.key}})
			if err != nil {
				t.Fatal(err)
			}
			if _, bad := resp.FieldErrors[keyBaseURL]; bad != c.wantURLError {
				t.Errorf("url error = %v, want %v (%v)", bad, c.wantURLError, resp.FieldErrors)
			}
			if _, bad := resp.FieldErrors[keyAPIKey]; bad != c.wantKeyError {
				t.Errorf("key error = %v, want %v (%v)", bad, c.wantKeyError, resp.FieldErrors)
			}
			if c.wantBase != "" {
				tg, err := p.api.target()
				if err != nil || tg.base != c.wantBase {
					t.Errorf("base = %q (%v), want %q", tg.base, err, c.wantBase)
				}
			}
		})
	}
}

func TestUnconfiguredIsFailedPrecondition(t *testing.T) {
	p := newPlugin()
	_, err := p.SearchWorks(ctx, &pluginv1.SearchWorksRequest{Query: "x"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestDescribe(t *testing.T) {
	p := newPlugin()
	m, err := p.Describe(ctx, &pluginv1.DescribeRequest{Language: "zh-TW"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.GetWorkActions()) != 1 || m.GetWorkActions()[0].GetId() != "request" || m.GetWorkActions()[0].GetRequiredProviderIds()[0] != "tmdb" {
		t.Errorf("work actions = %v", m.GetWorkActions())
	}
	if len(m.GetPermissions()) != 2 || m.GetPermissions()[0] != "network.lan" || m.GetPermissions()[1] != "discover.seeds" || len(m.GetDiscoverSeedIds()) != 1 || m.GetDiscoverSeedIds()[0] != "tmdb" || len(m.GetFeatures()) != 0 {
		t.Errorf("permissions %v features %v", m.GetPermissions(), m.GetFeatures())
	}
	if strings.Contains(strings.ToLower(m.GetAttribution().GetText()), "tmdb") {
		t.Error("the attribution must not name TMDB")
	}
	for _, f := range m.GetConfigFields() {
		if f.GetKey() == keyAPIKey && f.GetType() != pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_SECRET {
			t.Error("the API key must be a secret field")
		}
	}
}

func TestTMDBLanguage(t *testing.T) {
	for in, want := range map[string]string{"": "en", "en": "en", "en-US": "en-US", "zh-TW": "zh-TW", "zh-Hant": "zh-TW", "zh-Hans": "zh-CN", "zh": "zh-CN", "ja_JP": "ja-JP", "pt-br": "pt-BR", "!!": "en"} {
		if got := tmdbLanguage(in); got != want {
			t.Errorf("tmdbLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearch(t *testing.T) {
	p, f := newTestPlugin(t)
	resp, err := p.SearchWorks(ctx, &pluginv1.SearchWorksRequest{Query: " a b ", PageToken: "2", Type: pluginv1.ItemType_ITEM_TYPE_MOVIE})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetWorks()) != 1 || resp.GetWorks()[0].GetKey() != "movie:603" || resp.GetNextPageToken() != "" {
		t.Errorf("response = %v", resp)
	}
	if q := f.last(t).query; !strings.Contains(q, "page=2") || !strings.Contains(q, "query=a%20b") {
		t.Errorf("query = %s", q)
	}
	n := len(f.reqs)
	empty, err := p.SearchWorks(ctx, &pluginv1.SearchWorksRequest{Query: "  "})
	if err != nil || len(empty.GetWorks()) != 0 || len(f.reqs) != n {
		t.Errorf("an empty query must answer an empty list without asking Seerr: %v %v", empty, err)
	}
}

func TestGetWork(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["tv:1396"] = `{"id":1396,"name":"Series B","firstAirDate":"2008-01-20","episodeRunTime":[47],"externalIds":{"imdbId":"tt0903747","tvdbId":81189},
	 "contentRatings":{"results":[{"iso_3166_1":"DE","rating":"16"},{"iso_3166_1":"US","rating":"TV-MA"}]},
	 "seasons":[{"seasonNumber":0,"name":"Specials","episodeCount":2},{"seasonNumber":1,"name":"Season 1","episodeCount":7,"airDate":"2008-01-20"}]}`
	f.details["movie:603"] = `{"id":603,"title":"Movie A","runtime":136,"externalIds":{"imdbId":"tt0133093"},
	 "releases":{"results":[{"iso_3166_1":"US","release_dates":[{"certification":""},{"certification":"R"}]}]}}`
	w, err := p.GetWork(ctx, &pluginv1.GetWorkRequest{Key: "tv:1396", Languages: langs("en")})
	if err != nil {
		t.Fatal(err)
	}
	if w.GetRuntimeMinutes() != 47 || w.GetContentRating() != "TV-MA" || w.GetProviderIds()["imdb"] != "tt0903747" || w.GetProviderIds()["tvdb"] != "81189" ||
		w.GetProviderIds()["tmdb"] != "1396" || len(w.GetSeasons()) != 2 || w.GetSeasons()[1].GetEpisodeCount() != 7 || w.GetSeasons()[1].GetYear() != 2008 {
		t.Errorf("series = %v", w)
	}
	m, err := p.GetWork(ctx, &pluginv1.GetWorkRequest{Key: "movie:603"})
	if err != nil || m.GetContentRating() != "R" || m.GetRuntimeMinutes() != 136 || len(m.GetSeasons()) != 0 {
		t.Errorf("movie = %v (%v)", m, err)
	}
	for _, key := range []string{"movie:9", "bogus", "tv:01", "movie:-1", "tv:"} {
		if _, err := p.GetWork(ctx, &pluginv1.GetWorkRequest{Key: key}); status.Code(err) != codes.NotFound {
			t.Errorf("GetWork(%q) = %v, want NOT_FOUND", key, err)
		}
	}
}

func TestSeerrErrors(t *testing.T) {
	p, _ := newTestPlugin(t)
	p.api.set(p.api.base, "wrong", "")
	if _, err := p.SearchWorks(ctx, &pluginv1.SearchWorksRequest{Query: "x"}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("rejected key: %v, want FAILED_PRECONDITION", err)
	}
	p.api.set("http://127.0.0.1:1", "k", "")
	if _, err := p.SearchWorks(ctx, &pluginv1.SearchWorksRequest{Query: "x"}); status.Code(err) != codes.Unavailable {
		t.Errorf("unreachable: %v, want UNAVAILABLE", err)
	}
}

func TestRunCheck(t *testing.T) {
	p, _ := newTestPlugin(t)
	srvURL := p.api.base
	run := func(url, key string) *pluginv1.CheckResult {
		resp, err := p.RunCheck(ctx, &pluginv1.RunCheckRequest{Id: checkConnect, Language: "en", Values: map[string]string{keyBaseURL: url, keyAPIKey: key}})
		if err != nil || len(resp.GetResults()) != 1 {
			t.Fatalf("RunCheck: %v %v", resp, err)
		}
		return resp.GetResults()[0]
	}
	if r := run(srvURL, "k"); r.GetStatus() != pluginv1.CheckStatus_CHECK_STATUS_OK || !strings.Contains(r.GetMessage(), "3.5.1") {
		t.Errorf("good: %v", r)
	}
	if r := run(srvURL, "bad"); r.GetStatus() != pluginv1.CheckStatus_CHECK_STATUS_FAILED || !strings.Contains(r.GetMessage(), "API key") {
		t.Errorf("bad key: %v", r)
	}
	if r := run("http://127.0.0.1:1", "k"); r.GetStatus() != pluginv1.CheckStatus_CHECK_STATUS_FAILED {
		t.Errorf("no server: %v", r)
	}
	if r := run("nonsense", ""); r.GetStatus() != pluginv1.CheckStatus_CHECK_STATUS_FAILED {
		t.Errorf("no values: %v", r)
	}
	if _, err := p.RunCheck(ctx, &pluginv1.RunCheckRequest{Id: "other"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("unknown check: %v", err)
	}
}

func TestCheckExplainsLocalNames(t *testing.T) {
	p, _ := newTestPlugin(t)
	check := func(base, lang string) string {
		c, cancel := context.WithTimeout(ctx, 500*time.Millisecond) // a .local lookup can hang; the reason is the same
		defer cancel()
		r, err := p.RunCheck(c, &pluginv1.RunCheckRequest{Id: "test_connection", Language: lang, Values: map[string]string{keyBaseURL: base, keyAPIKey: "k"}})
		if err != nil || len(r.GetResults()) != 1 || r.GetResults()[0].GetStatus() != pluginv1.CheckStatus_CHECK_STATUS_FAILED {
			t.Fatalf("%s: %v %v", base, r, err)
		}
		return r.GetResults()[0].GetMessage()
	}
	for _, base := range []string{"http://seerr.local:5055", "http://NAS.Local.:5055"} {
		if got := check(base, "en"); !strings.Contains(got, ".local") {
			t.Errorf("%s: %q, want the .local reason", base, got)
		}
	}
	if got := check("http://seerr.local:5055", "zh-TW"); !strings.Contains(got, ".local") {
		t.Errorf("zh-TW: %q", got)
	}
	if got := check("http://127.0.0.1:1", "en"); strings.Contains(got, ".local") {
		t.Errorf("an IP address got the .local reason: %q", got)
	}
}
