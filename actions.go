package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	pluginapi "github.com/kryspal/plugin-sdk"
	"github.com/kryspal/plugin-sdk/i18n"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// seasonChoice is one season of a series as the member sees it in the request dialog.
type seasonChoice struct {
	number   int
	noteKey  string // "" when the season can be requested
	disabled bool
}

// assessment is what Seerr says about a title, from one member's point of view.
type assessment struct {
	status     pluginv1.WorkStatus
	textKey    string
	canRequest bool
	label      string // "" keeps the declared button text
	seasons    []seasonChoice
	// std and uhd are the standard and the 4K side of a title, set only when Seerr has a 4K server for the type: then the request dialog asks
	// which one, and the fields above say what holds for the title as a whole (the better status, whether either side can be requested).
	std, uhd *assessment
}

var statusText = map[pluginv1.WorkStatus]string{
	pluginv1.WorkStatus_WORK_STATUS_PENDING:    "stPending",
	pluginv1.WorkStatus_WORK_STATUS_PROCESSING: "stProcessing",
	pluginv1.WorkStatus_WORK_STATUS_PARTIAL:    "stPartial",
	pluginv1.WorkStatus_WORK_STATUS_AVAILABLE:  "stAvailable",
	pluginv1.WorkStatus_WORK_STATUS_DECLINED:   "stDeclined",
	pluginv1.WorkStatus_WORK_STATUS_FAILED:     "stFailed",
}

// assess maps Seerr's media and request status onto the fixed set of states. memberID is the member's Seerr user, 0 when unknown; a declined
// or failed request only counts for the member who made it. is4k says which version of the title is judged: Seerr keeps the standard and the
// 4K version apart, each with its own requests, status and seasons.
//
// Media status: 1 unknown and 7 deleted count as nothing; 2 awaiting approval; 3 processing; 4 partly available; 5 available; 6 blocklisted
// counts as nothing and cannot be requested. Request status: 3 declined (the title can be requested again), 4 failed.
func assess(kind string, r result, memberID int, is4k bool) assessment {
	mi := r.MediaInfo
	if mi == nil {
		mi = &mediaInfo{}
	}
	var active, declined, failed bool
	var pending, approved bool
	requested := map[int]bool{}
	for _, rq := range mi.Requests {
		if rq.Is4K != is4k {
			continue
		}
		switch rq.Status {
		case requestPending, requestApproved:
			active = true
			pending = pending || rq.Status == requestPending
			approved = approved || rq.Status == requestApproved
			for _, s := range rq.Seasons {
				requested[s.SeasonNumber] = true
			}
		case requestDeclined:
			declined = declined || (memberID != 0 && rq.RequestedBy.ID == memberID)
		case requestFailed:
			failed = failed || (memberID != 0 && rq.RequestedBy.ID == memberID)
		}
	}
	a := assessment{status: pluginv1.WorkStatus_WORK_STATUS_NONE}
	whole := true // may a new request be made at all
	switch mi.tierStatus(is4k) {
	case mediaAvailable:
		a.status, whole = pluginv1.WorkStatus_WORK_STATUS_AVAILABLE, false
	case mediaPartial:
		a.status = pluginv1.WorkStatus_WORK_STATUS_PARTIAL
	case mediaProcessing:
		a.status, whole = pluginv1.WorkStatus_WORK_STATUS_PROCESSING, false
	case mediaPending:
		a.status, whole = pluginv1.WorkStatus_WORK_STATUS_PENDING, false
	case mediaBlocklisted:
		whole = false
	default: // unknown, deleted, or a value this plugin does not know
		switch {
		case approved:
			a.status, whole = pluginv1.WorkStatus_WORK_STATUS_PROCESSING, false
		case pending:
			a.status, whole = pluginv1.WorkStatus_WORK_STATUS_PENDING, false
		case declined:
			a.status = pluginv1.WorkStatus_WORK_STATUS_DECLINED
		case failed:
			a.status = pluginv1.WorkStatus_WORK_STATUS_FAILED
		}
	}
	a.textKey = statusText[a.status]
	again := a.status == pluginv1.WorkStatus_WORK_STATUS_DECLINED || a.status == pluginv1.WorkStatus_WORK_STATUS_FAILED
	if kind == kindMovie {
		a.canRequest = whole && !active && a.status != pluginv1.WorkStatus_WORK_STATUS_PARTIAL
		if again {
			a.label = "actionAgain"
		}
		return a
	}
	if mi.tierStatus(is4k) == mediaBlocklisted {
		return a
	}
	// A series stays open to the seasons nobody has asked for, whatever its overall state: the seasons below decide.
	have := map[int]int{}
	for _, s := range mi.tierSeasons(is4k) {
		have[s.SeasonNumber] = s.Status
	}
	blocked := false
	for _, s := range r.Seasons {
		if s.SeasonNumber == 0 {
			continue // Seerr does not create a request for the specials alone
		}
		c := seasonChoice{number: s.SeasonNumber}
		switch st := have[s.SeasonNumber]; {
		case st == mediaAvailable || mi.tierStatus(is4k) == mediaAvailable:
			c.disabled, c.noteKey = true, "noteAvailable"
		case st == mediaPartial:
			c.disabled, c.noteKey = true, "notePartial"
		case st == mediaPending || st == mediaProcessing || requested[s.SeasonNumber]:
			c.disabled, c.noteKey = true, "noteRequested"
		}
		if c.disabled {
			blocked = true
		} else {
			a.canRequest = true
		}
		a.seasons = append(a.seasons, c)
	}
	switch {
	case again:
		a.label = "actionAgain"
	case blocked || a.status == pluginv1.WorkStatus_WORK_STATUS_PARTIAL:
		a.label = "actionMore"
	}
	return a
}

// statusRank orders the states by how far the title has come, for choosing the one to show when the standard and the 4K side differ.
func statusRank(s pluginv1.WorkStatus) int {
	switch s {
	case pluginv1.WorkStatus_WORK_STATUS_AVAILABLE:
		return 5
	case pluginv1.WorkStatus_WORK_STATUS_PARTIAL:
		return 4
	case pluginv1.WorkStatus_WORK_STATUS_PROCESSING:
		return 3
	case pluginv1.WorkStatus_WORK_STATUS_PENDING:
		return 2
	case pluginv1.WorkStatus_WORK_STATUS_NONE:
		return 0
	}
	return 1 // declined, failed
}

// assessAll is assess for a title whose type may have a 4K server (has4k). Without one it is the standard side alone. With one the title
// shows the better of the two states and can be requested while either side can; each side keeps its own assessment for the dialog.
func assessAll(kind string, r result, memberID int, has4k bool) assessment {
	std := assess(kind, r, memberID, false)
	if !has4k {
		return std
	}
	uhd := assess(kind, r, memberID, true)
	out := std
	out.std, out.uhd = &std, &uhd
	if statusRank(uhd.status) > statusRank(std.status) {
		out.status, out.textKey = uhd.status, uhd.textKey
	}
	out.canRequest = std.canRequest || uhd.canRequest
	if !std.canRequest && uhd.canRequest {
		out.label = uhd.label
		if out.label == "" && std.status != pluginv1.WorkStatus_WORK_STATUS_NONE {
			out.label = "actionMore"
		}
	}
	// A season can be asked for when it is open on either side; the side chosen decides when the request is made.
	out.seasons = nil
	for i, c := range std.seasons {
		if c.disabled && i < len(uhd.seasons) && !uhd.seasons[i].disabled {
			c = uhd.seasons[i]
		}
		out.seasons = append(out.seasons, c)
	}
	return out
}

// qualityOption asks which version to request: standard or 4K. A side that cannot be requested is shown, disabled, with the reason.
func (a assessment) qualityOption(t i18n.Translator) *pluginv1.WorkOption {
	opt := &pluginv1.WorkOption{Key: optQuality, Label: t.T("optQuality"), Required: true}
	picked := false
	for _, side := range []struct {
		value, label string
		a            *assessment
	}{{qualityStandard, "qualityStandard", a.std}, {quality4K, "quality4K", a.uhd}} {
		c := &pluginv1.WorkChoice{Value: side.value, Label: t.T(side.label), Disabled: !side.a.canRequest}
		if c.Disabled {
			switch side.a.status {
			case pluginv1.WorkStatus_WORK_STATUS_AVAILABLE:
				c.Note = t.T("noteAvailable")
			case pluginv1.WorkStatus_WORK_STATUS_PARTIAL:
				c.Note = t.T("notePartial")
			default:
				c.Note = t.T("noteRequested")
			}
		} else if !picked {
			c.Selected, picked = true, true
		}
		opt.Choices = append(opt.Choices, c)
	}
	return opt
}

// state builds the WorkState of one assessment; with offers it also says what the member can do.
func (a assessment) state(ref string, t i18n.Translator, offers bool) *pluginv1.WorkState {
	st := &pluginv1.WorkState{Ref: ref, Status: a.status}
	if a.textKey != "" {
		st.StatusText = t.T(a.textKey)
	}
	if !offers || !a.canRequest {
		return st
	}
	offer := &pluginv1.WorkActionOffer{Action: actionRequest}
	if a.label != "" {
		offer.Label = t.T(a.label)
	}
	if a.uhd != nil {
		offer.Options = append(offer.Options, a.qualityOption(t))
	}
	if len(a.seasons) > 0 {
		opt := &pluginv1.WorkOption{Key: "seasons", Label: t.T("optSeasons"), Multiple: true, Required: true}
		for i, s := range a.seasons {
			if i >= pluginapi.MaxWorkChoices {
				break
			}
			c := &pluginv1.WorkChoice{Value: strconv.Itoa(s.number), Label: genericSeason(t, s.number), Disabled: s.disabled,
				Selected: !s.disabled}
			if s.noteKey != "" {
				c.Note = t.T(s.noteKey)
			}
			opt.Choices = append(opt.Choices, c)
		}
		offer.Options = append(offer.Options, opt)
	}
	st.Actions = []*pluginv1.WorkActionOffer{offer}
	return st
}

// detail returns Seerr's view of one title. fresh skips the few-second cache.
func (p *plugin) detail(ctx context.Context, tg target, kind string, id int, fresh bool) (result, error) {
	key := workKey(kind, id)
	if !fresh {
		if r, ok := p.cache.get(key); ok {
			return r, nil
		}
	}
	var r result
	if err := p.api.get(ctx, tg, "/"+kind+"/"+strconv.Itoa(id), nil, &r); err != nil {
		return result{}, err
	}
	p.cache.put(key, r)
	return r, nil
}

func refKind(w *pluginv1.WorkRef) (kind string, id int, ok bool) {
	kind = kindOf(w.GetType())
	n, err := strconv.Atoi(w.GetProviderIds()[providerTMDB])
	if kind == "" || err != nil || n <= 0 {
		return "", 0, false
	}
	return kind, n, true
}

// GetWorkStates reports the state of up to 50 titles. It asks Seerr once per title, a few at a time. A title Seerr cannot answer for is left
// out; when none can be answered the error is returned, so the viewer still sees the titles without state.
func (p *plugin) GetWorkStates(ctx context.Context, req *pluginv1.GetWorkStatesRequest) (*pluginv1.GetWorkStatesResponse, error) {
	tg, err := p.api.target()
	if err != nil {
		return nil, err
	}
	t := messages.For(req.GetLanguages().GetLanguage())
	refs := req.GetWorks()
	if len(refs) > pluginapi.MaxWorkStates {
		refs = refs[:pluginapi.MaxWorkStates]
	}
	details := make([]result, len(refs))
	errs := make([]error, len(refs))
	var wg sync.WaitGroup
	for i, w := range refs {
		kind, id, ok := refKind(w)
		if !ok {
			errs[i] = sdk.NotFound("no TMDB number")
			continue
		}
		// Badges only: what the lists said a moment ago is enough unless the member's own declined or failed request could matter.
		if !req.GetIncludeActions() {
			if e, ok := p.states.get(kind, id); ok && (!e.present || (e.status != mediaUnknown && e.status != mediaDeleted)) {
				details[i] = result{ID: id, MediaType: kind}
				if e.present {
					details[i].MediaInfo = &mediaInfo{Status: e.status}
				}
				continue
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			details[i], errs[i] = p.detail(ctx, tg, kind, id, req.GetIncludeActions())
		}()
	}
	wg.Wait()
	memberID := p.lazyMember(ctx, tg, req.GetMember())
	resp := &pluginv1.GetWorkStatesResponse{}
	var firstErr error
	for i, w := range refs {
		if errs[i] != nil {
			if firstErr == nil && grpcstatus.Code(errs[i]) != codes.NotFound {
				firstErr = errs[i]
			}
			continue
		}
		kind, _, _ := refKind(w)
		resp.States = append(resp.States, assessAll(kind, details[i], memberID(details[i]), p.has4k(ctx, tg, kind)).state(w.GetRef(), t, req.GetIncludeActions()))
	}
	if len(resp.States) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return resp, nil
}

// lazyMember returns a function that finds the member's Seerr user only for titles that have a declined or failed request (the member's own
// declined request is the only thing a state needs the user for). A member Seerr does not know gets 0.
func (p *plugin) lazyMember(ctx context.Context, tg target, m *pluginv1.WorkMember) func(result) int {
	var once sync.Once
	id := 0
	return func(r result) int {
		if r.MediaInfo == nil {
			return 0
		}
		need := false
		for _, rq := range r.MediaInfo.Requests {
			need = need || rq.Status == requestDeclined || rq.Status == requestFailed
		}
		if !need || m.GetId() == "" {
			return 0
		}
		once.Do(func() { id, _ = p.findUser(ctx, tg, m) })
		return id
	}
}

// recheckUser drops what is remembered about a member and finds the Seerr user again. It is for a call made for that user that Seerr refused
// with 403: the account may be gone. 0 means the member has no Seerr account (any more). Only the call without X-API-User can say that the
// API key itself was refused, and it returns that error.
func (p *plugin) recheckUser(ctx context.Context, tg target, m *pluginv1.WorkMember) (int, error) {
	p.users.forget(m.GetId())
	return p.findUser(ctx, tg, m)
}

// findUser finds the Seerr user for a member by sign-in name; 0 when there is none or it is not unique. The match goes from the most to the
// least reliable name: the Jellyfin or Emby sign-in name (a member who signed in to Seerr with their server account), the Seerr user name,
// the Plex name. Seerr's user search does not look at display names (v3.5.1), so they are not matched.
func (p *plugin) findUser(ctx context.Context, tg target, m *pluginv1.WorkMember) (int, error) {
	name := strings.TrimSpace(m.GetName())
	if name == "" {
		return 0, nil
	}
	if id, ok := p.users.get(m.GetId()); ok {
		return id, nil
	}
	var out struct {
		Results []struct {
			ID               int    `json:"id"`
			Username         string `json:"username"`
			JellyfinUsername string `json:"jellyfinUsername"`
			PlexUsername     string `json:"plexUsername"`
		} `json:"results"`
	}
	if err := p.api.get(ctx, tg, "/user", url.Values{"q": {name}, "take": {"50"}}, &out); err != nil {
		return 0, err
	}
	for _, field := range []func(i int) string{
		func(i int) string { return out.Results[i].JellyfinUsername },
		func(i int) string { return out.Results[i].Username },
		func(i int) string { return out.Results[i].PlexUsername },
	} {
		var hits []int
		for i := range out.Results {
			if strings.EqualFold(strings.TrimSpace(field(i)), name) {
				hits = append(hits, out.Results[i].ID)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			p.users.put(m.GetId(), hits[0])
			return hits[0], nil
		}
		return 0, nil // two users share this name: do not guess
	}
	return 0, nil
}

// RunWorkAction makes the request for the member. The call to Seerr carries the member's Seerr user ID in X-API-User, so approval and quota
// are the member's own. Everything the member can fix or must know is a refusal; only a broken setup or an unreachable Seerr is an error.
func (p *plugin) RunWorkAction(ctx context.Context, req *pluginv1.RunWorkActionRequest) (*pluginv1.RunWorkActionResponse, error) {
	if req.GetAction() != actionRequest {
		return nil, sdk.InvalidArgument("unknown action")
	}
	kind, id, ok := refKind(req.GetWork())
	if !ok {
		return nil, sdk.InvalidArgument("the work has no TMDB number")
	}
	tg, err := p.api.target()
	if err != nil {
		return nil, err
	}
	t := messages.For(req.GetLanguages().GetLanguage())
	refuse := func(key string) (*pluginv1.RunWorkActionResponse, error) {
		return &pluginv1.RunWorkActionResponse{Refused: true, Message: t.T(key)}, nil
	}
	uid, err := p.findUser(ctx, tg, req.GetMember())
	if err != nil {
		return nil, err
	}
	if uid == 0 {
		return &pluginv1.RunWorkActionResponse{Refused: true, Message: t.T("refNoAccount"),
			Link: &pluginv1.ExternalLink{Url: linkURL(tg), Label: t.T("linkOpen")}}, nil
	}
	r, err := p.detail(ctx, tg, kind, id, true)
	if err != nil {
		return nil, err
	}
	has4k := p.has4k(ctx, tg, kind)
	all := assessAll(kind, r, uid, has4k)
	a, is4k := all, false
	if q := chosenQuality(req.GetSelections()); has4k {
		switch q {
		case "", qualityStandard:
			a = *all.std
		case quality4K:
			a, is4k = *all.uhd, true
		default:
			return refuse("refCannot")
		}
	} else if q == quality4K {
		return refuse("refCannot") // Seerr has no 4K server for this type
	}
	if !a.canRequest {
		return refuse("refCannot")
	}
	body := map[string]any{"mediaType": kind, "mediaId": id}
	if has4k {
		body["is4k"] = is4k
	}
	if kind == kindTV {
		seasons, ok := chosenSeasons(req.GetSelections(), a)
		if !ok {
			return refuse("refSeasons")
		}
		body["seasons"] = seasons
	}
	resp, err := p.api.post(ctx, tg, "/request", uid, body)
	if err != nil {
		return nil, err
	}
	p.cache.forget(workKey(kind, id))
	p.states.forget(kind, id)
	switch {
	case resp.Status == http.StatusForbidden:
		if strings.Contains(strings.ToLower(errorMessage(resp.Body)), "quota") {
			return refuse("refQuota")
		}
		// Not a quota: the account may have been deleted since it was remembered.
		if now, err := p.recheckUser(ctx, tg, req.GetMember()); err != nil {
			return nil, err
		} else if now == 0 {
			return &pluginv1.RunWorkActionResponse{Refused: true, Message: t.T("refNoAccount"),
				Link: &pluginv1.ExternalLink{Url: linkURL(tg), Label: t.T("linkOpen")}}, nil
		}
		return refuse("refForbidden")
	case resp.Status == http.StatusConflict:
		return refuse("refExists")
	case resp.Status == http.StatusUnauthorized:
		return nil, sdk.NeedsConfiguration("seerr: the API key was refused (401)")
	case resp.Status == http.StatusNotFound:
		return nil, sdk.NotFound("seerr: not found")
	case resp.Status < 200 || resp.Status >= 300:
		return nil, statusError(resp)
	}
	var made struct {
		ID     int `json:"id"`
		Status int `json:"status"`
	}
	if json.Unmarshal(resp.Body, &made) != nil || made.ID <= 0 {
		return nil, sdk.Unavailable("seerr: the answer holds no request")
	}
	out := &pluginv1.RunWorkActionResponse{Message: t.T("okApproved")}
	if made.Status == requestPending {
		out.Message = t.T("okPending")
	}
	if fresh, err := p.detail(ctx, tg, kind, id, true); err == nil {
		out.State = assessAll(kind, fresh, uid, has4k).state("", t, true)
	}
	return out, nil
}

// Keys and values of the quality question.
const (
	optQuality      = "quality"
	qualityStandard = "standard"
	quality4K       = "4k"
)

// chosenQuality reads the answer to the quality question; "" when there is none.
func chosenQuality(sel []*pluginv1.WorkSelection) string {
	for _, s := range sel {
		if s.GetKey() == optQuality && len(s.GetValues()) == 1 {
			return s.GetValues()[0]
		}
	}
	return ""
}

// chosenSeasons reads the answer to the seasons question and keeps it to seasons that could be requested. ok is false when nothing valid is left.
func chosenSeasons(sel []*pluginv1.WorkSelection, a assessment) ([]int, bool) {
	allowed := map[int]bool{}
	for _, s := range a.seasons {
		if !s.disabled {
			allowed[s.number] = true
		}
	}
	seen := map[int]bool{}
	var out []int
	for _, s := range sel {
		if s.GetKey() != "seasons" {
			continue
		}
		for _, v := range s.GetValues() {
			n, err := strconv.Atoi(v)
			if err != nil || !allowed[n] || seen[n] {
				return nil, false
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out, len(out) > 0
}

// errorMessage reads the "message" Seerr puts in an error answer.
func errorMessage(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Message
}

// linkURL is the Seerr address members see: the public one when set, else the address the server uses.
func linkURL(tg target) string {
	if tg.publicURL != "" {
		return tg.publicURL
	}
	return tg.base
}
