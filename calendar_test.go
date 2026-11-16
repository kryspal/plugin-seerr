package main

import (
	"context"
	"testing"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const calMovie = `{"id":700,"title":"Film X","releaseDate":"2026-12-01","releases":{"results":[
 {"iso_3166_1":"US","release_dates":[{"type":1,"release_date":"2026-11-01T00:00:00.000Z"},{"type":3,"release_date":"2026-12-05T00:00:00.000Z"},{"type":4,"release_date":"2027-02-01T00:00:00.000Z"},{"type":6,"release_date":"2026-12-06T00:00:00.000Z"}]},
 {"iso_3166_1":"TW","release_dates":[{"type":3,"release_date":"2026-12-03T00:00:00.000Z"},{"type":5,"release_date":"2026-12-20T00:00:00.000Z"}]},
 {"iso_3166_1":"JP","release_dates":[{"type":4,"release_date":"2026-12-10T00:00:00.000Z"},{"type":4,"release_date":"bad"}]}]}}`

const calSeries = `{"id":800,"name":"Show Y","firstAirDate":"2020-01-01","status":"Returning Series",
 "nextEpisodeToAir":{"airDate":"2026-11-20","seasonNumber":2,"episodeNumber":1,"name":"Again"}}`

const calSeason = `{"episodes":[
 {"episodeNumber":1,"airDate":"2026-11-20","name":"Again"},
 {"episodeNumber":2,"airDate":"2026-11-27","name":"Twice"},
 {"episodeNumber":3,"airDate":"2027-03-01","name":"Far"},
 {"episodeNumber":0,"airDate":"2026-11-10","name":"Aired already"}]}`

func calendarPlugin(t *testing.T) (*plugin, *fakeSeerr) {
	p, f := newTestPlugin(t)
	f.details["movie:700"] = calMovie
	f.details["tv:800"] = calSeries
	f.details["tv:800/season/2"] = calSeason
	f.details["movie:701"] = `{"id":701,"title":"Library film","releases":{"results":[{"iso_3166_1":"US","release_dates":[{"type":3,"release_date":"2026-12-05T00:00:00.000Z"}]}]}}`
	f.details["movie:702"] = `{"id":702,"title":"Declined film","releases":{"results":[{"iso_3166_1":"US","release_dates":[{"type":3,"release_date":"2026-12-05T00:00:00.000Z"}]}]}}`
	f.requests = `{"pageInfo":{"pages":1,"pageSize":100,"results":5,"page":1},"results":[
	 {"id":1,"type":"movie","status":2,"requestedBy":{"id":5},"media":{"tmdbId":700,"status":3}},
	 {"id":2,"type":"tv","status":2,"requestedBy":{"id":5},"media":{"tmdbId":800,"status":4}},
	 {"id":3,"type":"movie","status":2,"requestedBy":{"id":5},"media":{"tmdbId":701,"status":5}},
	 {"id":4,"type":"movie","status":3,"requestedBy":{"id":5},"media":{"tmdbId":702,"status":1}},
	 {"id":5,"type":"movie","status":2,"requestedBy":{"id":5},"media":{"tmdbId":700,"status":3}}]}`
	return p, f
}

func calendar(t *testing.T, p *plugin, lang, from, to string) *pluginv1.ListMemberCalendarResponse {
	t.Helper()
	resp, err := p.ListMemberCalendar(context.Background(), &pluginv1.ListMemberCalendarRequest{Member: mia, Languages: langs(lang), From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type calRow struct {
	date, key string
	kind      pluginv1.CalendarEntryKind
	season    int32
	episode   int32
}

func rows(r *pluginv1.ListMemberCalendarResponse) []calRow {
	var out []calRow
	for _, e := range r.Entries {
		out = append(out, calRow{e.Date, e.Work.Key, e.Kind, e.Season, e.Episode})
	}
	return out
}

func TestCalendarMovieDatesFollowTheRegion(t *testing.T) {
	p, f := calendarPlugin(t)
	// en: the United States; the premiere (type 1) and the TV release (type 6) are not listed; the digital date is the US one.
	got := rows(calendar(t, p, "en", "2026-11-15", "2027-01-15"))
	got = append(got, rows(calendar(t, p, "en", "2027-01-20", "2027-03-20"))...)
	want := []calRow{
		{"2026-11-20", "tv:800", pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_SEASON_PREMIERE, 2, 1},
		{"2026-11-27", "tv:800", pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_EPISODE, 2, 2},
		{"2026-12-05", "movie:700", pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_THEATRICAL, 0, 0},
		{"2026-12-20", "movie:700", pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_PHYSICAL, 0, 0},
		{"2027-02-01", "movie:700", pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_DIGITAL, 0, 0},
		{"2027-03-01", "tv:800", pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_EPISODE, 2, 3},
	}
	if len(got) != len(want) {
		t.Fatalf("en: %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("en[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	// zh-TW: the Taiwan theatrical date wins over the US one.
	for _, r := range rows(calendar(t, p, "zh-TW", "2026-12-01", "2026-12-31")) {
		if r.key == "movie:700" && r.kind == pluginv1.CalendarEntryKind_CALENDAR_ENTRY_KIND_THEATRICAL && r.date != "2026-12-03" {
			t.Errorf("zh-TW theatrical date = %s, want 2026-12-03", r.date)
		}
	}
	// A title is looked up once per language, however many windows are asked.
	if n := f.countExact("/api/v1/movie/700"); n != 2 {
		t.Errorf("movie 700 looked up %d times, want 2 (en and zh-TW)", n)
	}
}

func TestCalendarLeavesOutWhatIsDoneOrDeclined(t *testing.T) {
	p, _ := calendarPlugin(t)
	for _, r := range rows(calendar(t, p, "en", "2026-11-15", "2027-01-15")) {
		if r.key == "movie:701" || r.key == "movie:702" {
			t.Errorf("%s is listed, but it is available or declined", r.key)
		}
	}
}

func TestCalendarWindowAndAskedSeason(t *testing.T) {
	p, f := calendarPlugin(t)
	// A window before the next episode: no season lookup at all.
	if got := rows(calendar(t, p, "en", "2026-11-15", "2026-11-19")); len(got) != 0 {
		t.Errorf("early window: %v", got)
	}
	if n := f.count("/api/v1/tv/800/season"); n != 0 {
		t.Errorf("season asked %d times for a window before the next episode", n)
	}
	// Twice the same window: the season is asked once.
	calendar(t, p, "en", "2026-11-15", "2026-12-31")
	calendar(t, p, "en", "2026-11-15", "2026-12-31")
	if n := f.count("/api/v1/tv/800/season"); n != 1 {
		t.Errorf("season asked %d times, want 1", n)
	}
	// Entries carry the member's own header on the request list only; the member is the one the user lookup found.
	if r := f.last(t); r.user == "" && r.path == "/api/v1/request" {
		t.Error("the request list was asked without the member")
	}
}

func TestCalendarBadWindowAndUnknownMember(t *testing.T) {
	p, _ := calendarPlugin(t)
	for _, c := range [][2]string{{"x", "2026-12-01"}, {"2026-12-01", "x"}, {"2026-12-05", "2026-12-01"}, {"2026-01-01", "2026-03-31"}} {
		_, err := p.ListMemberCalendar(context.Background(), &pluginv1.ListMemberCalendarRequest{Member: mia, From: c[0], To: c[1]})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("window %v: %v, want InvalidArgument", c, err)
		}
	}
	resp, err := p.ListMemberCalendar(context.Background(), &pluginv1.ListMemberCalendarRequest{Member: &pluginv1.WorkMember{Id: "x", Name: "Zed"}, From: "2026-12-01", To: "2026-12-31"})
	if err != nil || len(resp.Entries) != 0 {
		t.Errorf("unknown member: %v, %v", resp, err)
	}
}

func TestCalendarNoteIsTranslated(t *testing.T) {
	p, _ := calendarPlugin(t)
	for lang, want := range map[string]string{"en": "Requested", "zh-TW": "已請求", "zh-CN": "已请求"} {
		resp := calendar(t, p, lang, "2026-12-01", "2026-12-31")
		if len(resp.Entries) == 0 || resp.Entries[0].Note != want {
			t.Errorf("%s: %v, want note %q", lang, resp.Entries, want)
		}
	}
}
