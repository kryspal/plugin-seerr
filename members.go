package main

import (
	"context"
	"net/url"
	"strconv"
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
// account has no requests. Seerr's answer holds no titles, so they are looked up like the popular rows' (cached, a few at a time, with a budget);
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
	if err := p.api.getAs(ctx, tg, uid, "/request", q, &list); err != nil {
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
		a := assess(kind, result{MediaInfo: &mediaInfo{Status: rq.Media.Status, Requests: []request{rq}}}, uid)
		mw := &pluginv1.MemberWork{Work: w, Status: a.status}
		if a.textKey != "" {
			mw.StatusText = t.T(a.textKey)
		}
		if _, err := time.Parse(time.RFC3339, rq.UpdatedAt); err == nil {
			mw.UpdatedAt = rq.UpdatedAt
		}
		if a.status == pluginv1.WorkStatus_WORK_STATUS_PROCESSING {
			if pct, ok := downloadPercent(rq.Media); ok {
				mw.ProgressPercent = &pct
			}
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

// downloadPercent is how much of what the download tool is fetching for this title is done. Seerr lists it only when it is connected to a
// download manager (Radarr or Sonarr) and the item is in its queue; otherwise there is nothing to show.
func downloadPercent(m *mediaInfo) (float64, bool) {
	var size, left float64
	for _, d := range m.DownloadStatus {
		size += d.Size
		left += d.SizeLeft
	}
	if size <= 0 || left < 0 || left > size {
		return 0, false
	}
	return (size - left) / size * 100, true
}
