package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func movieRef(ref string) *pluginv1.WorkRef {
	return &pluginv1.WorkRef{Ref: ref, Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, ProviderIds: map[string]string{"tmdb": "603"}}
}

func tvRef(ref string) *pluginv1.WorkRef {
	return &pluginv1.WorkRef{Ref: ref, Type: pluginv1.ItemType_ITEM_TYPE_SERIES, ProviderIds: map[string]string{"tmdb": "1396", "imdb": "tt1"}}
}

var mia = &pluginv1.WorkMember{Id: "m-1", Name: "Mia"}

func movieWith(mi string) string {
	if mi == "" {
		return `{"id":603,"title":"Movie A"}`
	}
	return `{"id":603,"title":"Movie A","mediaInfo":` + mi + `}`
}

func tvWith(mi string) string {
	s := `{"id":1396,"name":"Series B","seasons":[{"seasonNumber":0,"episodeCount":2},{"seasonNumber":1,"episodeCount":7},{"seasonNumber":2,"episodeCount":8},{"seasonNumber":3,"episodeCount":8}]`
	if mi != "" {
		s += `,"mediaInfo":` + mi
	}
	return s + `}`
}

func TestMovieStates(t *testing.T) {
	cases := []struct {
		name        string
		mediaInfo   string
		want        pluginv1.WorkStatus
		text        string
		wantRequest bool
		label       string
	}{
		{"no media info", "", pluginv1.WorkStatus_WORK_STATUS_NONE, "", true, ""},
		{"unknown", `{"status":1}`, pluginv1.WorkStatus_WORK_STATUS_NONE, "", true, ""},
		{"deleted counts as none", `{"status":7}`, pluginv1.WorkStatus_WORK_STATUS_NONE, "", true, ""},
		{"blocklisted", `{"status":6}`, pluginv1.WorkStatus_WORK_STATUS_NONE, "", false, ""},
		{"pending", `{"status":2,"requests":[{"status":1,"requestedBy":{"id":6}}]}`, pluginv1.WorkStatus_WORK_STATUS_PENDING, "Awaiting approval", false, ""},
		{"processing", `{"status":3,"requests":[{"status":2}]}`, pluginv1.WorkStatus_WORK_STATUS_PROCESSING, "Processing", false, ""},
		{"partial", `{"status":4}`, pluginv1.WorkStatus_WORK_STATUS_PARTIAL, "Partly available", false, ""},
		{"available", `{"status":5}`, pluginv1.WorkStatus_WORK_STATUS_AVAILABLE, "Available", false, ""},
		{"declined for this member", `{"status":1,"requests":[{"status":3,"requestedBy":{"id":5}}]}`, pluginv1.WorkStatus_WORK_STATUS_DECLINED, "Declined", true, "Ask again"},
		{"declined for another member", `{"status":1,"requests":[{"status":3,"requestedBy":{"id":6}}]}`, pluginv1.WorkStatus_WORK_STATUS_NONE, "", true, ""},
		{"failed for this member", `{"status":1,"requests":[{"status":4,"requestedBy":{"id":5}}]}`, pluginv1.WorkStatus_WORK_STATUS_FAILED, "Failed", true, "Ask again"},
		{"a 4K request is ignored", `{"status":1,"requests":[{"status":1,"is4k":true,"requestedBy":{"id":6}}]}`, pluginv1.WorkStatus_WORK_STATUS_NONE, "", true, ""},
		{"approved request, media not updated yet", `{"status":1,"requests":[{"status":2}]}`, pluginv1.WorkStatus_WORK_STATUS_PROCESSING, "Processing", false, ""},
		{"declined, then asked again and pending", `{"status":2,"requests":[{"status":3,"requestedBy":{"id":5}},{"status":1,"requestedBy":{"id":5}}]}`, pluginv1.WorkStatus_WORK_STATUS_PENDING, "Awaiting approval", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, f := newTestPlugin(t)
			f.details["movie:603"] = movieWith(c.mediaInfo)
			for _, withActions := range []bool{false, true} {
				resp, err := p.GetWorkStates(ctx, &pluginv1.GetWorkStatesRequest{Works: []*pluginv1.WorkRef{movieRef("a")}, Member: mia, Languages: langs("en"), IncludeActions: withActions})
				if err != nil || len(resp.GetStates()) != 1 {
					t.Fatalf("states: %v %v", resp, err)
				}
				st := resp.GetStates()[0]
				if st.GetRef() != "a" || st.GetStatus() != c.want || st.GetStatusText() != c.text {
					t.Errorf("state = %v, want %v %q", st, c.want, c.text)
				}
				if !withActions {
					if len(st.GetActions()) != 0 {
						t.Errorf("badges carry actions: %v", st.GetActions())
					}
					continue
				}
				if got := len(st.GetActions()) == 1; got != c.wantRequest {
					t.Fatalf("offers = %v, want request offered: %v", st.GetActions(), c.wantRequest)
				}
				if c.wantRequest && (st.GetActions()[0].GetAction() != "request" || st.GetActions()[0].GetLabel() != c.label || len(st.GetActions()[0].GetOptions()) != 0) {
					t.Errorf("offer = %v", st.GetActions()[0])
				}
			}
		})
	}
}

func TestSeriesOffer(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["tv:1396"] = tvWith(`{"status":4,"seasons":[{"seasonNumber":1,"status":5},{"seasonNumber":2,"status":3}],"requests":[{"status":1,"requestedBy":{"id":6},"seasons":[{"seasonNumber":2,"status":1}]}]}`)
	resp, err := p.GetWorkStates(ctx, &pluginv1.GetWorkStatesRequest{Works: []*pluginv1.WorkRef{tvRef("s")}, Member: mia, Languages: langs("en"), IncludeActions: true})
	if err != nil {
		t.Fatal(err)
	}
	st := resp.GetStates()[0]
	if st.GetStatus() != pluginv1.WorkStatus_WORK_STATUS_PARTIAL {
		t.Errorf("status = %v", st.GetStatus())
	}
	o := st.GetActions()[0]
	if o.GetLabel() != "Request more" || len(o.GetOptions()) != 1 {
		t.Fatalf("offer = %v", o)
	}
	opt := o.GetOptions()[0]
	if opt.GetKey() != "seasons" || !opt.GetMultiple() || !opt.GetRequired() || len(opt.GetChoices()) != 3 {
		t.Fatalf("option = %v", opt)
	}
	want := []struct {
		value, label, note string
		disabled, selected bool
	}{
		{"1", "Season 1", "Available", true, false},
		{"2", "Season 2", "Requested", true, false},
		{"3", "Season 3", "", false, true},
	}
	for i, w := range want {
		c := opt.GetChoices()[i]
		if c.GetValue() != w.value || c.GetLabel() != w.label || c.GetNote() != w.note || c.GetDisabled() != w.disabled || c.GetSelected() != w.selected {
			t.Errorf("choice %d = %v, want %+v", i, c, w)
		}
	}
}

func TestSeriesOfferEdges(t *testing.T) {
	cases := []struct {
		name, mediaInfo string
		offered         bool
		label           string
		selected        []string
	}{
		{"nothing yet", "", true, "", []string{"1", "2", "3"}},
		{"all available", `{"status":5,"seasons":[{"seasonNumber":1,"status":5}]}`, false, "", nil},
		{"every season awaiting approval", `{"status":2,"requests":[{"status":1,"seasons":[{"seasonNumber":1},{"seasonNumber":2},{"seasonNumber":3}]}]}`, false, "", nil},
		{"two seasons awaiting approval, the rest open", `{"status":2,"requests":[{"status":1,"seasons":[{"seasonNumber":1},{"seasonNumber":2}]}]}`, true, "Request more", []string{"3"}},
		{"two seasons processing, the rest open", `{"status":3,"seasons":[{"seasonNumber":1,"status":3},{"seasonNumber":2,"status":3}]}`, true, "Request more", []string{"3"}},
		{"all seasons requested", `{"status":4,"requests":[{"status":2,"seasons":[{"seasonNumber":1},{"seasonNumber":2},{"seasonNumber":3}]}]}`, false, "", nil},
		{"declined", `{"status":1,"requests":[{"status":3,"requestedBy":{"id":5},"seasons":[{"seasonNumber":1}]}]}`, true, "Ask again", []string{"1", "2", "3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, f := newTestPlugin(t)
			f.details["tv:1396"] = tvWith(c.mediaInfo)
			resp, err := p.GetWorkStates(ctx, &pluginv1.GetWorkStatesRequest{Works: []*pluginv1.WorkRef{tvRef("s")}, Member: mia, Languages: langs("en"), IncludeActions: true})
			if err != nil {
				t.Fatal(err)
			}
			acts := resp.GetStates()[0].GetActions()
			if (len(acts) == 1) != c.offered {
				t.Fatalf("offers = %v, want offered: %v", acts, c.offered)
			}
			if !c.offered {
				return
			}
			if acts[0].GetLabel() != c.label {
				t.Errorf("label = %q, want %q", acts[0].GetLabel(), c.label)
			}
			var sel []string
			for _, ch := range acts[0].GetOptions()[0].GetChoices() {
				if ch.GetSelected() {
					sel = append(sel, ch.GetValue())
				}
			}
			if strings.Join(sel, ",") != strings.Join(c.selected, ",") {
				t.Errorf("selected = %v, want %v", sel, c.selected)
			}
		})
	}
}

func TestStatesBatchAndErrors(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith(`{"status":5}`)
	// tv:1396 is unknown to the fake: that work is left out, the other is answered.
	resp, err := p.GetWorkStates(ctx, &pluginv1.GetWorkStatesRequest{Works: []*pluginv1.WorkRef{movieRef("a"), tvRef("b"),
		{Ref: "c", Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, ProviderIds: map[string]string{"imdb": "tt1"}}}, Member: mia})
	if err != nil || len(resp.GetStates()) != 1 || resp.GetStates()[0].GetRef() != "a" {
		t.Fatalf("states = %v (%v)", resp, err)
	}
	p.api.set(p.api.base, "wrong", "")
	p.cache.clear()
	if _, err := p.GetWorkStates(ctx, &pluginv1.GetWorkStatesRequest{Works: []*pluginv1.WorkRef{movieRef("a")}, Member: mia}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("rejected key: %v, want FAILED_PRECONDITION", err)
	}
}

func TestStatesCache(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith("")
	req := &pluginv1.GetWorkStatesRequest{Works: []*pluginv1.WorkRef{movieRef("a")}, Member: mia}
	for range 3 {
		if _, err := p.GetWorkStates(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count("/api/v1/movie/"); n != 1 {
		t.Errorf("Seerr was asked %d times for three badge calls, want 1", n)
	}
}

func TestRunMovie(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith("")
	resp, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: mia, Languages: langs("en")})
	if err != nil || resp.GetRefused() {
		t.Fatalf("run: %v %v", resp, err)
	}
	posts := f.posts()
	if len(posts) != 1 {
		t.Fatalf("posts = %v", posts)
	}
	if posts[0].user != "5" {
		t.Errorf("X-API-User = %q, want the member's Seerr user 5", posts[0].user)
	}
	if posts[0].path != "/api/v1/request" || posts[0].body != `{"mediaId":603,"mediaType":"movie"}` {
		t.Errorf("post = %+v", posts[0])
	}
	if resp.GetMessage() != "Request sent. It is waiting for approval." || resp.GetState() == nil {
		t.Errorf("response = %v", resp)
	}
}

func TestRunSeries(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["tv:1396"] = tvWith(`{"status":4,"seasons":[{"seasonNumber":1,"status":5}]}`)
	run := func(sel ...*pluginv1.WorkSelection) *pluginv1.RunWorkActionResponse {
		resp, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: tvRef(""), Member: mia, Languages: langs("en"), Selections: sel})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	pick := func(v ...string) *pluginv1.WorkSelection {
		return &pluginv1.WorkSelection{Key: "seasons", Values: v}
	}
	for name, resp := range map[string]*pluginv1.RunWorkActionResponse{
		"no answer":               run(),
		"empty answer":            run(pick()),
		"a season that is there":  run(pick("1")),
		"an unknown season":       run(pick("9")),
		"not a number":            run(pick("x")),
		"one bad among good ones": run(pick("2", "1")),
		"the same season twice":   run(pick("2", "2")),
		"another question's key":  run(&pluginv1.WorkSelection{Key: "other", Values: []string{"2"}}),
	} {
		if !resp.GetRefused() || resp.GetMessage() == "" {
			t.Errorf("%s: want a refusal with a message, got %v", name, resp)
		}
	}
	if len(f.posts()) != 0 {
		t.Fatalf("a refused answer reached Seerr: %v", f.posts())
	}
	resp := run(pick("3", "2"))
	if resp.GetRefused() {
		t.Fatalf("refused: %v", resp)
	}
	if posts := f.posts(); len(posts) != 1 || posts[0].body != `{"mediaId":1396,"mediaType":"tv","seasons":[2,3]}` {
		t.Errorf("posts = %v", posts)
	}
}

func TestRunRefusals(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		member     *pluginv1.WorkMember
		details    string
		wantInText string
		wantLink   bool
	}{
		{"no Seerr account", 0, "", &pluginv1.WorkMember{Id: "x", Name: "Zed"}, movieWith(""), "Sign in to Seerr once", true},
		{"two users share the name", 0, "", &pluginv1.WorkMember{Id: "y", Name: "dup"}, movieWith(""), "Sign in to Seerr once", true},
		{"quota used up", http.StatusForbidden, `{"message":"Movie Quota exceeded."}`, mia, movieWith(""), "request quota", false},
		{"no request permission", http.StatusForbidden, `{"message":"You do not have permission to make this request"}`, mia, movieWith(""), "does not allow", false},
		{"already requested", http.StatusConflict, `{"message":"Request for this media already exists."}`, mia, movieWith(""), "already been requested", false},
		{"available since the badge", 0, "", mia, movieWith(`{"status":5}`), "cannot be requested", false},
		{"awaiting approval since the badge", 0, "", mia, movieWith(`{"status":2}`), "cannot be requested", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, f := newTestPlugin(t)
			f.users = `{"results":[{"id":5,"username":"mia","jellyfinUsername":"mia"},{"id":7,"jellyfinUsername":"Dup"},{"id":8,"jellyfinUsername":"dup"}]}`
			f.details["movie:603"] = c.details
			if c.status != 0 {
				f.postStatus, f.postBody = c.status, c.body
			}
			resp, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: c.member, Languages: langs("en")})
			if err != nil {
				t.Fatal(err)
			}
			if !resp.GetRefused() || !strings.Contains(resp.GetMessage(), c.wantInText) {
				t.Errorf("response = %v, want a refusal containing %q", resp, c.wantInText)
			}
			if (resp.GetLink() != nil) != c.wantLink {
				t.Errorf("link = %v, want link: %v", resp.GetLink(), c.wantLink)
			}
			if c.wantLink && resp.GetLink().GetUrl() != p.api.base {
				t.Errorf("link = %v, want the Seerr address", resp.GetLink())
			}
			if c.status == 0 && len(f.posts()) != 0 {
				t.Errorf("a request was sent to Seerr: %v", f.posts())
			}
		})
	}
}

func TestRunErrors(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith("")
	if _, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "delete", Work: movieRef(""), Member: mia}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("unknown action: %v", err)
	}
	if _, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: &pluginv1.WorkRef{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE}, Member: mia}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no TMDB number: %v", err)
	}
	f.postStatus, f.postBody = http.StatusUnauthorized, `{}`
	if _, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: mia}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("401: %v, want FAILED_PRECONDITION", err)
	}
	f.postStatus = http.StatusBadGateway
	if _, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: mia}); status.Code(err) != codes.Unavailable {
		t.Errorf("502: %v, want UNAVAILABLE", err)
	}
}

func TestUserLookupIsCachedAndPerMember(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith("")
	for range 2 {
		if _, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: mia}); err != nil {
			t.Fatal(err)
		}
	}
	lookups := 0
	for _, r := range f.reqs {
		if r.path == "/api/v1/user" {
			lookups++
			if !strings.Contains(r.query, "q=Mia") {
				t.Errorf("lookup = %s", r.query)
			}
		}
	}
	if lookups != 1 {
		t.Errorf("user lookups = %d, want 1", lookups)
	}
	bob := &pluginv1.WorkMember{Id: "m-2", Name: "BOB"}
	if _, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: bob}); err != nil {
		t.Fatal(err)
	}
	if posts := f.posts(); posts[len(posts)-1].user != "6" {
		t.Errorf("X-API-User = %q, want 6 for the second member (case-insensitive match)", posts[len(posts)-1].user)
	}
}

func TestSpecialsAreNotOffered(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["tv:1396"] = tvWith("")
	resp, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: tvRef(""), Member: mia,
		Selections: []*pluginv1.WorkSelection{{Key: "seasons", Values: []string{"0"}}}})
	if err != nil || !resp.GetRefused() || len(f.posts()) != 0 {
		t.Errorf("specials only: %v %v posts %v", resp, err, f.posts())
	}
}

func TestAnswerWithoutRequestIsNotSuccess(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith("")
	f.postBody = `{}`
	if _, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: mia}); status.Code(err) != codes.Unavailable {
		t.Errorf("an answer with no request: %v, want UNAVAILABLE", err)
	}
}

func TestPublicAddressInRefusal(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith("")
	p.api.set(p.api.base, "k", "https://seerr.example.net")
	resp, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: &pluginv1.WorkMember{Id: "z", Name: "Zed"}})
	if err != nil || resp.GetLink().GetUrl() != "https://seerr.example.net" {
		t.Errorf("link = %v (%v)", resp.GetLink(), err)
	}
}

func badgeStates(t *testing.T, p *plugin, refs ...*pluginv1.WorkRef) map[string]*pluginv1.WorkState {
	t.Helper()
	resp, err := p.GetWorkStates(ctx, &pluginv1.GetWorkStatesRequest{Works: refs, Member: mia, Languages: langs("en")})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*pluginv1.WorkState{}
	for _, s := range resp.GetStates() {
		out[s.GetRef()] = s
	}
	return out
}

// A list answer carries mediaInfo.status, so the badges of a page of works need no call per title.
func TestBadgesUseWhatTheListsSaid(t *testing.T) {
	p, f := newTestPlugin(t)
	f.list = `{"page":1,"totalPages":1,"results":[
	 {"id":603,"mediaType":"movie","title":"A","mediaInfo":{"status":5}},
	 {"id":1396,"mediaType":"tv","name":"B","mediaInfo":{"status":3}},
	 {"id":700,"mediaType":"movie","title":"C"},
	 {"id":701,"mediaType":"movie","title":"D","mediaInfo":{"status":1}}]}`
	f.details["movie:701"] = movieWith(`{"status":1,"requests":[{"status":3,"requestedBy":{"id":5}}]}`)
	if _, err := p.GetDiscoverPage(ctx, &pluginv1.GetDiscoverPageRequest{Ref: "movies", RowId: "popular_movies", Languages: langs("en")}); err != nil {
		t.Fatal(err)
	}
	ref := func(r string, ty pluginv1.ItemType, id string) *pluginv1.WorkRef {
		return &pluginv1.WorkRef{Ref: r, Type: ty, ProviderIds: map[string]string{"tmdb": id}}
	}
	m, tv := pluginv1.ItemType_ITEM_TYPE_MOVIE, pluginv1.ItemType_ITEM_TYPE_SERIES
	got := badgeStates(t, p, ref("a", m, "603"), ref("b", tv, "1396"), ref("c", m, "700"), ref("d", m, "701"))
	if got["a"].GetStatus() != pluginv1.WorkStatus_WORK_STATUS_AVAILABLE || got["b"].GetStatus() != pluginv1.WorkStatus_WORK_STATUS_PROCESSING ||
		got["c"].GetStatus() != pluginv1.WorkStatus_WORK_STATUS_NONE {
		t.Errorf("states = %v", got)
	}
	// Only the title whose status the list cannot settle (unknown: a declined request is possible) is asked about.
	if got["d"].GetStatus() != pluginv1.WorkStatus_WORK_STATUS_DECLINED {
		t.Errorf("d = %v, want the member's declined request found", got["d"])
	}
	if n := f.count("/api/v1/movie/") + f.count("/api/v1/tv/"); n != 1 {
		t.Errorf("%d detail calls, want 1 (title D)", n)
	}
}

func TestBadgesFallBackToDetailsAndRequestsInvalidateThem(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith(`{"status":5}`)
	// Never seen in a list: asked per title.
	if got := badgeStates(t, p, movieRef("a")); got["a"].GetStatus() != pluginv1.WorkStatus_WORK_STATUS_AVAILABLE || f.count("/api/v1/movie/") != 1 {
		t.Fatalf("unseen title: %v, %d calls", got, f.count("/api/v1/movie/"))
	}
	// Seen, then old: asked again.
	p.states.record([]result{{ID: 603, MediaType: kindMovie}})
	p.states.ttl = time.Nanosecond
	time.Sleep(5 * time.Millisecond)
	p.cache.clear()
	_ = badgeStates(t, p, movieRef("a"))
	if f.count("/api/v1/movie/") != 2 {
		t.Errorf("an old list answer must not be used: %d calls", f.count("/api/v1/movie/"))
	}
	// A request made through the plugin makes the remembered status void.
	p.states.ttl = time.Hour
	p.states.record([]result{{ID: 603, MediaType: kindMovie}})
	f.details["movie:603"] = movieWith("")
	if _, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: mia}); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.states.get(kindMovie, 603); ok {
		t.Error("the status remembered from a list survived a request")
	}
}
