package main

import (
	"strings"
	"testing"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

const (
	servers4K    = `[{"id":1,"is4k":false},{"id":2,"is4k":true}]`
	serversPlain = `[{"id":1,"is4k":false}]`
)

func offerOf(t *testing.T, p *plugin, ref *pluginv1.WorkRef) *pluginv1.WorkActionOffer {
	t.Helper()
	resp, err := p.GetWorkStates(ctx, &pluginv1.GetWorkStatesRequest{Works: []*pluginv1.WorkRef{ref}, Member: mia, Languages: langs("en"), IncludeActions: true})
	if err != nil || len(resp.GetStates()) != 1 {
		t.Fatalf("states: %v %v", resp, err)
	}
	if len(resp.GetStates()[0].GetActions()) == 0 {
		return nil
	}
	return resp.GetStates()[0].GetActions()[0]
}

func TestNoQualityQuestionWithoutA4KServer(t *testing.T) {
	for name, radarr := range map[string]string{"no servers answer": "", "only a standard server": serversPlain} {
		p, f := newTestPlugin(t)
		f.radarr = radarr
		f.details["movie:603"] = movieWith("")
		if o := offerOf(t, p, movieRef("")); o == nil || len(o.GetOptions()) != 0 {
			t.Errorf("%s: offer = %v, want a request with no question", name, o)
		}
		if resp, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: mia, Languages: langs("en")}); err != nil || resp.GetRefused() {
			t.Fatalf("%s: run %v %v", name, resp, err)
		}
		if posts := f.posts(); len(posts) != 1 || strings.Contains(posts[0].body, "is4k") {
			t.Errorf("%s: posts = %v, want no is4k", name, posts)
		}
		// a 4K answer without a 4K server is not honoured
		if resp, _ := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: mia, Languages: langs("en"),
			Selections: []*pluginv1.WorkSelection{{Key: "quality", Values: []string{"4k"}}}}); !resp.GetRefused() {
			t.Errorf("%s: a 4K request with no 4K server was made: %v", name, resp)
		}
	}
}

func TestQualityQuestion(t *testing.T) {
	p, f := newTestPlugin(t)
	f.radarr = servers4K
	f.details["movie:603"] = movieWith("")
	o := offerOf(t, p, movieRef(""))
	if o == nil || len(o.GetOptions()) != 1 {
		t.Fatalf("offer = %v", o)
	}
	q := o.GetOptions()[0]
	if q.GetKey() != "quality" || !q.GetRequired() || q.GetMultiple() || len(q.GetChoices()) != 2 {
		t.Fatalf("option = %v", q)
	}
	std, uhd := q.GetChoices()[0], q.GetChoices()[1]
	if std.GetValue() != "standard" || std.GetLabel() != "Standard" || !std.GetSelected() || std.GetDisabled() ||
		uhd.GetValue() != "4k" || uhd.GetLabel() != "4K" || uhd.GetSelected() || uhd.GetDisabled() {
		t.Errorf("choices = %v %v", std, uhd)
	}
	for tag, want := range map[string][3]string{"zh-TW": {"畫質", "標準", "4K"}, "zh-CN": {"画质", "标准", "4K"}} {
		resp, _ := p.GetWorkStates(ctx, &pluginv1.GetWorkStatesRequest{Works: []*pluginv1.WorkRef{movieRef("")}, Member: mia, Languages: langs(tag), IncludeActions: true})
		q := resp.GetStates()[0].GetActions()[0].GetOptions()[0]
		if q.GetLabel() != want[0] || q.GetChoices()[0].GetLabel() != want[1] || q.GetChoices()[1].GetLabel() != want[2] {
			t.Errorf("%s: %v", tag, q)
		}
	}
	pick := func(v string) []*pluginv1.WorkSelection {
		return []*pluginv1.WorkSelection{{Key: "quality", Values: []string{v}}}
	}
	run := func(sel []*pluginv1.WorkSelection) *pluginv1.RunWorkActionResponse {
		resp, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: mia, Languages: langs("en"), Selections: sel})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := run(pick("bogus")); !resp.GetRefused() || len(f.posts()) != 0 {
		t.Fatalf("an unknown quality was accepted: %v", resp)
	}
	if resp := run(pick("4k")); resp.GetRefused() {
		t.Fatalf("4K refused: %v", resp)
	}
	if resp := run(pick("standard")); resp.GetRefused() {
		t.Fatalf("standard refused: %v", resp)
	}
	posts := f.posts()
	if len(posts) != 2 || posts[0].body != `{"is4k":true,"mediaId":603,"mediaType":"movie"}` || posts[1].body != `{"is4k":false,"mediaId":603,"mediaType":"movie"}` {
		t.Errorf("posts = %v", posts)
	}
}

func TestQualitySidesAreJudgedApart(t *testing.T) {
	p, f := newTestPlugin(t)
	f.radarr = servers4K
	// The standard version is available; the 4K one was never asked for.
	f.details["movie:603"] = movieWith(`{"status":5,"status4k":1}`)
	resp, _ := p.GetWorkStates(ctx, &pluginv1.GetWorkStatesRequest{Works: []*pluginv1.WorkRef{movieRef("")}, Member: mia, Languages: langs("en"), IncludeActions: true})
	st := resp.GetStates()[0]
	if st.GetStatus() != pluginv1.WorkStatus_WORK_STATUS_AVAILABLE || len(st.GetActions()) != 1 {
		t.Fatalf("state = %v", st)
	}
	o := st.GetActions()[0]
	std, uhd := o.GetOptions()[0].GetChoices()[0], o.GetOptions()[0].GetChoices()[1]
	if !std.GetDisabled() || std.GetNote() != "Available" || uhd.GetDisabled() || !uhd.GetSelected() || o.GetLabel() != "Request more" {
		t.Errorf("offer = %v (standard %v, 4K %v)", o, std, uhd)
	}
	// Asking for the standard version again is refused; the 4K one goes through.
	run := func(v string) *pluginv1.RunWorkActionResponse {
		r, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: movieRef(""), Member: mia, Languages: langs("en"),
			Selections: []*pluginv1.WorkSelection{{Key: "quality", Values: []string{v}}}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if !run("standard").GetRefused() || len(f.posts()) != 0 {
		t.Fatal("the standard version is available and was requested again")
	}
	if run("4k").GetRefused() || len(f.posts()) != 1 {
		t.Fatal("4K was refused")
	}
	// Both are done: nothing to offer.
	f.details["movie:603"] = movieWith(`{"status":5,"status4k":5}`)
	p.cache.clear()
	if o := offerOf(t, p, movieRef("")); o != nil {
		t.Errorf("offer = %v, want none", o)
	}
}

func TestStatusOfA4KOnlyTitle(t *testing.T) {
	p, f := newTestPlugin(t)
	f.radarr = servers4K
	// Requested in 4K alone: the standard side says nothing, the 4K side is being fetched.
	f.details["movie:603"] = movieWith(`{"status":1,"status4k":3,"requests":[{"status":2,"is4k":true,"requestedBy":{"id":5}}]}`)
	resp, _ := p.GetWorkStates(ctx, &pluginv1.GetWorkStatesRequest{Works: []*pluginv1.WorkRef{movieRef("")}, Member: mia, Languages: langs("en"), IncludeActions: true})
	st := resp.GetStates()[0]
	if st.GetStatus() != pluginv1.WorkStatus_WORK_STATUS_PROCESSING {
		t.Errorf("status = %v, want processing from the 4K side", st.GetStatus())
	}
	o := st.GetActions()[0].GetOptions()[0]
	if !o.GetChoices()[1].GetDisabled() || o.GetChoices()[1].GetNote() != "Requested" || o.GetChoices()[0].GetDisabled() {
		t.Errorf("quality = %v", o)
	}
	// Without a 4K server the same record shows nothing: that side does not count.
	f.radarr = serversPlain
	p.tiers.clear()
	p.cache.clear()
	resp, _ = p.GetWorkStates(ctx, &pluginv1.GetWorkStatesRequest{Works: []*pluginv1.WorkRef{movieRef("")}, Member: mia, Languages: langs("en")})
	if got := resp.GetStates()[0].GetStatus(); got != pluginv1.WorkStatus_WORK_STATUS_NONE {
		t.Errorf("status without a 4K server = %v", got)
	}
}

func TestSeriesQuality(t *testing.T) {
	p, f := newTestPlugin(t)
	f.sonarr = servers4K
	// Season 1 is available in standard, season 2 only in 4K, season 3 is open on both sides.
	f.details["tv:1396"] = tvWith(`{"status":4,"status4k":4,"seasons":[{"seasonNumber":1,"status":5}],"seasons4k":[{"seasonNumber":2,"status":5}]}`)
	o := offerOf(t, p, tvRef(""))
	if o == nil || len(o.GetOptions()) != 2 || o.GetOptions()[0].GetKey() != "quality" || o.GetOptions()[1].GetKey() != "seasons" {
		t.Fatalf("offer = %v", o)
	}
	for i, want := range []bool{false, false, false} { // every season is open on one side at least
		if c := o.GetOptions()[1].GetChoices()[i]; c.GetDisabled() != want {
			t.Errorf("season choice %d = %v", i, c)
		}
	}
	run := func(q string, seasons ...string) *pluginv1.RunWorkActionResponse {
		r, err := p.RunWorkAction(ctx, &pluginv1.RunWorkActionRequest{Action: "request", Work: tvRef(""), Member: mia, Languages: langs("en"),
			Selections: []*pluginv1.WorkSelection{{Key: "quality", Values: []string{q}}, {Key: "seasons", Values: seasons}}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if !run("standard", "1").GetRefused() || !run("4k", "2").GetRefused() || len(f.posts()) != 0 {
		t.Fatal("a season that side already has was requested")
	}
	if run("standard", "2").GetRefused() || run("4k", "1").GetRefused() {
		t.Fatal("a season open on that side was refused")
	}
	posts := f.posts()
	if len(posts) != 2 || posts[0].body != `{"is4k":false,"mediaId":1396,"mediaType":"tv","seasons":[2]}` || posts[1].body != `{"is4k":true,"mediaId":1396,"mediaType":"tv","seasons":[1]}` {
		t.Errorf("posts = %v", posts)
	}
}

func memberList(t *testing.T, p *plugin, f *fakeSeerr, body string) []*pluginv1.MemberWork {
	t.Helper()
	f.details["movie:603"] = `{"id":603,"title":"Movie A"}`
	f.requests = `{"pageInfo":{"pages":1,"page":1},"results":[` + body + `]}`
	resp, err := p.ListMemberWorks(ctx, &pluginv1.ListMemberWorksRequest{Member: mia, Languages: langs("en")})
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetWorks()
}

func TestMemberWorkOf4KRequestUses4KSide(t *testing.T) {
	p, f := newTestPlugin(t)
	ws := memberList(t, p, f, `{"id":1,"type":"movie","status":2,"is4k":true,"requestedBy":{"id":5},"media":{"tmdbId":603,"status":5,"status4k":3,
	 "downloadStatus":[{"size":100,"sizeLeft":0,"status":"completed"}],
	 "downloadStatus4k":[{"size":1000,"sizeLeft":900,"status":"downloading","downloadId":"d1","estimatedCompletionTime":"2026-11-06T10:00:00.000Z"}]}}`)
	w := ws[0]
	if w.GetStatus() != pluginv1.WorkStatus_WORK_STATUS_PROCESSING || w.ProgressPercent == nil || *w.ProgressPercent != 10 ||
		w.GetStage() != pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_DOWNLOADING || w.GetEstimatedCompletion() != "2026-11-06T10:00:00Z" {
		t.Errorf("work = %v", w)
	}
}

func TestSeasonPackCountsOnce(t *testing.T) {
	// One pack of 8 episodes: the queue lists the same download, with the pack's size, once per episode.
	pack := func(left string) string {
		var items []string
		for range 8 {
			items = append(items, `{"size":8000,"sizeLeft":`+left+`,"status":"downloading","downloadId":"pack1","title":"Series.S01"}`)
		}
		return strings.Join(items, ",")
	}
	p, f := newTestPlugin(t)
	ws := memberList(t, p, f, `{"id":1,"type":"movie","status":2,"requestedBy":{"id":5},"media":{"tmdbId":603,"status":3,"downloadStatus":[`+pack("6000")+`]}}`)
	if ws[0].ProgressPercent == nil || *ws[0].ProgressPercent != 25 {
		t.Errorf("progress = %v, want 25 (a quarter of one pack)", ws[0].ProgressPercent)
	}
	// Two different downloads still add up.
	ws = memberList(t, p, f, `{"id":1,"type":"movie","status":2,"requestedBy":{"id":5},"media":{"tmdbId":603,"status":3,"downloadStatus":[
	 {"size":1000,"sizeLeft":0,"status":"downloading","downloadId":"a"},{"size":3000,"sizeLeft":3000,"status":"downloading","downloadId":"b"},
	 {"size":3000,"sizeLeft":3000,"status":"downloading","downloadId":"b"}]}}`)
	if ws[0].ProgressPercent == nil || *ws[0].ProgressPercent != 25 {
		t.Errorf("progress = %v, want 25 (1000 of 4000)", ws[0].ProgressPercent)
	}
}

func TestUnknownSizeShowsNoPercent(t *testing.T) {
	p, f := newTestPlugin(t)
	ws := memberList(t, p, f, `{"id":1,"type":"movie","status":2,"requestedBy":{"id":5},"media":{"tmdbId":603,"status":3,"downloadStatus":[
	 {"size":1000,"sizeLeft":0,"status":"downloading","downloadId":"a"},{"size":0,"sizeLeft":0,"status":"queued","downloadId":"b"}]}}`)
	if ws[0].ProgressPercent != nil {
		t.Errorf("progress = %v, want none while one download has no known size", *ws[0].ProgressPercent)
	}
	if ws[0].GetStage() != pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_QUEUED {
		t.Errorf("stage = %v", ws[0].GetStage())
	}
}

func TestDownloadStagePriority(t *testing.T) {
	st := func(statuses ...string) pluginv1.MemberWorkStage {
		var ds []download
		for i, s := range statuses {
			ds = append(ds, download{Status: s, DownloadID: string(rune('a' + i))})
		}
		return downloadStageOf(ds)
	}
	for name, c := range map[string]struct {
		got  pluginv1.MemberWorkStage
		want pluginv1.MemberWorkStage
	}{
		"stalled beats paused and the rest": {st("downloading", "paused", "warning", "queued"), pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_STALLED},
		"paused beats the rest":             {st("downloading", "queued", "paused", "completed"), pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_PAUSED},
		"the earliest of the others":        {st("completed", "downloading", "queued"), pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_QUEUED},
		"downloading before importing":      {st("completed", "downloading"), pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_DOWNLOADING},
		"importing alone":                   {st("completed"), pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_IMPORTING},
		"unknown statuses are left out":     {st("whatever", "downloading"), pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_DOWNLOADING},
		"only unknown statuses":             {st("whatever", ""), pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_UNSPECIFIED},
		"no downloads":                      {st(), pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_UNSPECIFIED},
		"a client that is down is stalled":  {st("downloading", "downloadClientUnavailable"), pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_STALLED},
	} {
		if c.got != c.want {
			t.Errorf("%s: %v, want %v", name, c.got, c.want)
		}
	}
}

func TestDownloadETA(t *testing.T) {
	d := func(id, status, eta string) download {
		return download{DownloadID: id, Status: status, EstimatedCompletionTime: eta}
	}
	for name, c := range map[string]struct {
		ds   []download
		want string
	}{
		"the latest of several":                   {[]download{d("a", "downloading", "2026-11-06T10:00:00.000Z"), d("b", "downloading", "2026-11-06T12:30:00.000Z"), d("c", "queued", "2026-11-06T11:00:00.000Z")}, "2026-11-06T12:30:00Z"},
		"a zone is turned into UTC":               {[]download{d("a", "downloading", "2026-11-06T20:00:00+08:00")}, "2026-11-06T12:00:00Z"},
		"none given":                              {[]download{d("a", "downloading", "")}, ""},
		"null in Seerr's JSON is empty":           {[]download{d("a", "queued", "")}, ""},
		"the epoch is no estimate":                {[]download{d("a", "queued", "1970-01-01T00:00:00.000Z")}, ""},
		"not a time":                              {[]download{d("a", "queued", "soon")}, ""},
		"one still going with none makes it none": {[]download{d("a", "downloading", "2026-11-06T10:00:00Z"), d("b", "downloading", "")}, ""},
		"a finished one with none does not count": {[]download{d("a", "downloading", "2026-11-06T10:00:00Z"), d("b", "completed", "")}, "2026-11-06T10:00:00Z"},
		"only finished ones with none":            {[]download{d("a", "completed", "")}, ""},
		"no downloads":                            {nil, ""},
	} {
		if got := downloadETA(c.ds); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestStageAndETAOnlyWhileProcessing(t *testing.T) {
	p, f := newTestPlugin(t)
	// Pending (media status 2): downloads cannot be meant yet, whatever the queue says.
	ws := memberList(t, p, f, `{"id":1,"type":"movie","status":1,"requestedBy":{"id":5},"media":{"tmdbId":603,"status":2,"downloadStatus":[{"size":10,"sizeLeft":5,"status":"downloading","downloadId":"a","estimatedCompletionTime":"2026-11-06T10:00:00Z"}]}}`)
	w := ws[0]
	if w.GetStage() != pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_UNSPECIFIED || w.GetEstimatedCompletion() != "" || w.ProgressPercent != nil {
		t.Errorf("work = %v", w)
	}
	// Processing but nothing in the queue: no stage, no time.
	ws = memberList(t, p, f, `{"id":1,"type":"movie","status":2,"requestedBy":{"id":5},"media":{"tmdbId":603,"status":3,"downloadStatus":[]}}`)
	if w := ws[0]; w.GetStage() != pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_UNSPECIFIED || w.GetEstimatedCompletion() != "" {
		t.Errorf("work = %v", w)
	}
}
