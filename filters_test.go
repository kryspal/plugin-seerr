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

// Seerr answers 403 to a call made for a user it no longer has. That is this member's problem, not the API key's: the source stays up.
func TestDeletedSeerrAccountIsOnlyThatMembersProblem(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = `{"id":603,"title":"Movie A"}`
	f.requests = `{"pageInfo":{"pages":1,"pageSize":20,"results":1,"page":1},"results":[{"id":9,"type":"movie","status":2,"requestedBy":{"id":5},"media":{"tmdbId":603,"status":3}}]}`
	list := func(m *pluginv1.WorkMember) (*pluginv1.ListMemberWorksResponse, error) {
		return p.ListMemberWorks(ctx, &pluginv1.ListMemberWorksRequest{Member: m, Languages: langs("en")})
	}
	if r, err := list(mia); err != nil || len(r.GetWorks()) != 1 { // the mapping is remembered now
		t.Fatalf("before: %v %v", r, err)
	}
	// The account is deleted in Seerr: the user is gone from /user too.
	f.deleted = "5"
	f.users = `{"results":[{"id":6,"username":"bob","jellyfinUsername":"bob"}]}`
	r, err := list(mia)
	if err != nil || len(r.GetWorks()) != 0 {
		t.Errorf("deleted account: %v %v, want an empty list and no error", r, err)
	}
	if _, ok := p.users.get(mia.GetId()); ok {
		t.Errorf("the mapping of a deleted account is still remembered")
	}
	// Someone else is not affected, and the source answers.
	if _, err := list(&pluginv1.WorkMember{Id: "b", Name: "bob"}); err != nil {
		t.Errorf("another member: %v", err)
	}
	if _, err := p.SearchWorks(ctx, &pluginv1.SearchWorksRequest{Query: "x", Languages: langs("en")}); err != nil {
		t.Errorf("the source: %v", err)
	}
	// The account comes back (a new ID): found again.
	f.deleted = ""
	f.users = `{"results":[{"id":8,"username":"mia","jellyfinUsername":"mia"}]}`
	if _, err := list(mia); err != nil {
		t.Errorf("after the account is back: %v", err)
	}
	if id, ok := p.users.get(mia.GetId()); !ok || id != 8 {
		t.Errorf("mapping = %d %v, want the new ID", id, ok)
	}
}

func TestADeletedAccountCannotRequest(t *testing.T) {
	p, f := newTestPlugin(t)
	f.details["movie:603"] = movieWith("")
	req := &pluginv1.RunWorkActionRequest{Action: "request", Member: mia, Languages: langs("en"),
		Work: &pluginv1.WorkRef{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, ProviderIds: map[string]string{"tmdb": "603"}}}
	if _, err := p.findUser(ctx, p.mustTarget(t), mia); err != nil { // remember mia
		t.Fatal(err)
	}
	f.deleted = "5"
	f.users = `{"results":[]}`
	f.postStatus = 403
	f.postBody = `{"message":"Forbidden"}`
	resp, err := p.RunWorkAction(ctx, req)
	if err != nil || !resp.GetRefused() || resp.GetLink().GetUrl() == "" {
		t.Errorf("response = %v (%v), want the no-account refusal with the link", resp, err)
	}
}

// A refused API key is still reported as one: it is judged by the call made without X-API-User.
func TestRefusedKeyIsStillAKeyProblem(t *testing.T) {
	p, f := newTestPlugin(t)
	f.requests = `{"pageInfo":{"pages":1,"pageSize":20,"results":0,"page":1},"results":[]}`
	if _, err := p.findUser(ctx, p.mustTarget(t), mia); err != nil {
		t.Fatal(err)
	}
	tg := p.mustTarget(t)
	p.api.set(tg.base, "wrong", "")
	if _, err := p.ListMemberWorks(ctx, &pluginv1.ListMemberWorksRequest{Member: mia, Languages: langs("en")}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("err = %v, want FAILED_PRECONDITION", err)
	}
}

func (p *plugin) mustTarget(t *testing.T) target {
	t.Helper()
	tg, err := p.api.target()
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func TestGenreImage(t *testing.T) {
	five := []string{"/1.jpg", "/2.jpg", "/3.jpg", "/4.jpg", "/5.jpg"}
	if got := genreImage(27, five); got != imageBase+"w1280_filter(duotone,1F2937,D1D5DB)/5.jpg" {
		t.Errorf("five backdrops = %s", got)
	}
	if got := genreImage(28, five[:2]); got != imageBase+"w1280_filter(duotone,991B1B,FCA5A5)/1.jpg" {
		t.Errorf("two backdrops = %s", got)
	}
	if got := genreImage(999999, five[:1]); got != imageBase+"w1280_filter(duotone,1F2937,D1D5DB)/1.jpg" {
		t.Errorf("unknown genre = %s", got)
	}
	if genreImage(28, nil) != "" || genreImage(28, []string{""}) != "" {
		t.Error("no picture must stay empty")
	}
}
