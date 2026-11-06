package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	pluginapi "github.com/kryspal/plugin-sdk"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
)

const (
	// memberPage is how many requests one page asks Seerr for: the host shows 20 per page and merges several plugins.
	memberPage = 20
	// memberBudget is how long a page waits for the title lookups; the host's limit is 15 seconds with the queue.
	memberBudget = 6 * time.Second
)

// ListMemberWorks lists the member's own requests, last changed first. Seerr answers with only that member's requests when the call is made
// with X-API-User (checked on v3.5.1), whatever the page token says; the token carries an offset and nothing else. A member without a Seerr
// account has no requests. Seerr's answer holds no titles, so they are looked up (cached, a few at a time, with a budget);
// a request whose title is not ready in time is listed as untitled and has its title on the next page load.
func (p *plugin) ListMemberWorks(ctx context.Context, req *pluginv1.ListMemberWorksRequest) (*pluginv1.ListMemberWorksResponse, error) {
	tg, err := p.api.target()
	if err != nil {
		return nil, err
	}
	skip := 0
	if tok := req.GetPageToken(); tok != "" {
		if skip, err = strconv.Atoi(tok); err != nil || skip < 0 || skip > 100000 {
			return nil, sdk.InvalidArgument("bad page token")
		}
	}
	uid, err := p.findUser(ctx, tg, req.GetMember())
	if err != nil {
		return nil, err
	}
	resp := &pluginv1.ListMemberWorksResponse{}
	if uid == 0 {
		return resp, nil
	}
	var list struct {
		PageInfo struct {
			Page  int `json:"page"`
			Pages int `json:"pages"`
		} `json:"pageInfo"`
		Results []request `json:"results"`
	}
	q := url.Values{"take": {strconv.Itoa(memberPage)}, "skip": {strconv.Itoa(skip)}, "sort": {"modified"}, "sortDirection": {"desc"}}
	resp0, err := p.api.fetch(ctx, tg, uid, "/request", q)
	if err != nil {
		return nil, err
	}
	if resp0.Status == http.StatusForbidden {
		// Seerr refuses a call made for a user it no longer has (the account was deleted). That concerns this member only, and the API key is
		// judged by the call without X-API-User (findUser), so a refused key is still reported as one.
		if uid, err = p.recheckUser(ctx, tg, req.GetMember()); err != nil {
			return nil, err
		}
		if uid == 0 {
			return resp, nil
		}
		if resp0, err = p.api.fetch(ctx, tg, uid, "/request", q); err != nil {
			return nil, err
		}
		if resp0.Status == http.StatusForbidden {
			slog.Warn("seerr refused to list a member's requests", "member", req.GetMember().GetId())
			return resp, nil
		}
	}
	if err := decode(resp0, &list); err != nil {
		return nil, err
	}
	tag := req.GetLanguages().GetLanguage()
	lang := tmdbLanguage(tag)
	t := messages.For(tag)
	var waits []<-chan struct{}
	for _, rq := range list.Results {
		if kind, id, ok := requestTitle(rq); ok {
			if done := p.titles.start(p, tg, kind, id, lang); done != nil {
				waits = append(waits, done)
			}
		}
	}
	awaitLookups(ctx, waits, memberBudget)
	for _, rq := range list.Results {
		kind, id, ok := requestTitle(rq)
		if !ok || len(resp.Works) >= pluginapi.MaxWorksPerPage {
			continue
		}
		d, _ := p.titles.get(kind, id, lang)
		w := toWork(result{ID: id, Title: d.Title, OriginalTitle: d.OriginalTitle, ReleaseDate: d.ReleaseDate, Name: d.Name, OriginalName: d.OriginalName,
			FirstAirDate: d.FirstAirDate, Overview: d.Overview, PosterPath: d.PosterPath}, kind)
		if w == nil { // the lookup has not answered: the request is still shown
			w = &pluginv1.Work{Key: workKey(kind, id), Type: itemType(kind), Title: t.T("untitled"), ProviderIds: map[string]string{providerTMDB: strconv.Itoa(id)}}
		}
		// A 4K request is judged by the 4K side of the title, a standard one by the standard side.
		a := assess(kind, result{MediaInfo: &mediaInfo{Status: rq.Media.Status, Status4K: rq.Media.Status4K, Requests: []request{rq}}}, uid, rq.Is4K)
		mw := &pluginv1.MemberWork{Work: w, Status: a.status}
		if a.textKey != "" {
			mw.StatusText = t.T(a.textKey)
		}
		if _, err := time.Parse(time.RFC3339, rq.UpdatedAt); err == nil {
			mw.UpdatedAt = rq.UpdatedAt
		}
		if a.status == pluginv1.WorkStatus_WORK_STATUS_PROCESSING {
			ds := rq.Media.tierDownloads(rq.Is4K)
			if pct, ok := downloadPercent(ds); ok {
				mw.ProgressPercent = &pct
			}
			mw.Stage = downloadStageOf(ds)
			mw.EstimatedCompletion = downloadETA(ds)
		}
		resp.Works = append(resp.Works, mw)
	}
	if list.PageInfo.Page >= 1 && list.PageInfo.Page < list.PageInfo.Pages {
		resp.NextPageToken = strconv.Itoa(skip + memberPage)
	}
	return resp, nil
}

// requestTitle is the type and TMDB number of the title a request is for.
func requestTitle(rq request) (kind string, id int, ok bool) {
	if rq.Media == nil || rq.Media.TMDBID <= 0 || (rq.Type != kindMovie && rq.Type != kindTV) {
		return "", 0, false
	}
	return rq.Type, rq.Media.TMDBID, true
}

// uniqueDownloads is the downloads Seerr lists for a title, each once. A season pack is one download that the queue shows as one item per
// episode, all with the same downloadId and the size of the whole pack: counting every item would count the pack once per episode. Items
// without an id are told apart by their title and size, and items with neither all count.
func uniqueDownloads(ds []download) []download {
	seen := map[string]bool{}
	var out []download
	for _, d := range ds {
		key := d.DownloadID
		if key == "" && d.Title != "" {
			key = "t:" + d.Title + "|" + strconv.FormatFloat(d.Size, 'f', -1, 64)
		}
		if key != "" { // with neither an id nor a title there is nothing to tell that two items are one download
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, d)
	}
	return out
}

// downloadPercent is how much of what the download tool is fetching for this title is done. Seerr lists it only when it is connected to a
// download manager (Radarr or Sonarr) and the item is in its queue; otherwise there is nothing to show. When a download has no known size the
// share cannot be told, so there is no number rather than a wrong one.
func downloadPercent(ds []download) (float64, bool) {
	var size, left float64
	for _, d := range uniqueDownloads(ds) {
		if d.Size <= 0 || d.SizeLeft < 0 || d.SizeLeft > d.Size {
			return 0, false
		}
		size += d.Size
		left += d.SizeLeft
	}
	if size <= 0 {
		return 0, false
	}
	return (size - left) / size * 100, true
}

// downloadStage reads the queue status Radarr and Sonarr give (Seerr passes it on as it is).
func downloadStage(status string) pluginv1.MemberWorkStage {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "downloading":
		return pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_DOWNLOADING
	case "queued", "delay":
		return pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_QUEUED
	case "paused":
		return pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_PAUSED
	case "completed":
		// The files are in; what is left is the import into the library.
		return pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_IMPORTING
	case "warning", "failed", "downloadclientunavailable":
		return pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_STALLED
	}
	return pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_UNSPECIFIED
}

// downloadStageOf is the stage of the download that needs attention most when a title has several: stalled, then paused, then the earliest
// of the others (queued, downloading, importing). Downloads whose status is not known are left out; with none known there is no stage.
func downloadStageOf(ds []download) pluginv1.MemberWorkStage {
	best := pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_UNSPECIFIED
	rank := func(s pluginv1.MemberWorkStage) int {
		switch s {
		case pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_STALLED:
			return 0
		case pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_PAUSED:
			return 1
		}
		return 2 + int(s) // the enum is in the order of the download's life
	}
	for _, d := range uniqueDownloads(ds) {
		s := downloadStage(d.Status)
		if s == pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_UNSPECIFIED {
			continue
		}
		if best == pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_UNSPECIFIED || rank(s) < rank(best) {
			best = s
		}
	}
	return best
}

// downloadETA is when the last of the downloads is expected to be done, as RFC 3339 in UTC. It is empty when it cannot be said: a download
// that is still going and has no estimate makes the whole unknown (a finished download waiting for its import does not count), and so does an
// estimate that is not a time or is the zero time Seerr's empty value turns into.
func downloadETA(ds []download) string {
	var latest time.Time
	for _, d := range uniqueDownloads(ds) {
		var at time.Time
		if t, err := time.Parse(time.RFC3339, d.EstimatedCompletionTime); err == nil && t.Year() >= 2000 {
			at = t
		}
		if at.IsZero() {
			if downloadStage(d.Status) == pluginv1.MemberWorkStage_MEMBER_WORK_STAGE_IMPORTING {
				continue
			}
			return ""
		}
		if at.After(latest) {
			latest = at
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339)
}
