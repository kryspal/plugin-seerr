package main

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"unicode/utf8"

	pluginapi "github.com/kryspal/plugin-sdk"
	"github.com/kryspal/plugin-sdk/i18n"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
)

// A work's key is "movie:<TMDB number>" or "tv:<TMDB number>". It holds nothing secret.
const (
	kindMovie = "movie"
	kindTV    = "tv"
)

func workKey(kind string, id int) string { return kind + ":" + strconv.Itoa(id) }

// parseKey reads a key made by workKey; ok is false for anything else.
func parseKey(key string) (kind string, id int, ok bool) {
	k, n, found := strings.Cut(key, ":")
	if !found || (k != kindMovie && k != kindTV) {
		return "", 0, false
	}
	id, err := strconv.Atoi(n)
	if err != nil || id <= 0 || strconv.Itoa(id) != n {
		return "", 0, false
	}
	return k, id, true
}

func kindOf(t pluginv1.ItemType) string {
	switch t {
	case pluginv1.ItemType_ITEM_TYPE_MOVIE:
		return kindMovie
	case pluginv1.ItemType_ITEM_TYPE_SERIES:
		return kindTV
	}
	return ""
}

func itemType(kind string) pluginv1.ItemType {
	if kind == kindTV {
		return pluginv1.ItemType_ITEM_TYPE_SERIES
	}
	return pluginv1.ItemType_ITEM_TYPE_MOVIE
}

func language(l *pluginv1.Languages) string { return tmdbLanguage(l.GetLanguage()) }

// SearchWorks searches Seerr. It cannot filter by type, so a page may hold fewer works than Seerr's page size.
func (p *plugin) SearchWorks(ctx context.Context, req *pluginv1.SearchWorksRequest) (*pluginv1.SearchWorksResponse, error) {
	tg, err := p.api.target()
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(req.GetQuery())
	if query == "" {
		return &pluginv1.SearchWorksResponse{}, nil
	}
	pageNo, err := parsePage(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	q := url.Values{"query": {query}, "page": {strconv.Itoa(pageNo)}, "language": {language(req.GetLanguages())}}
	pg, err := p.page(ctx, tg, "/search", q)
	if err != nil {
		return nil, err
	}
	return &pluginv1.SearchWorksResponse{Works: works(pg.Results, kindOf(req.GetType()), messages.For(req.GetLanguages().GetLanguage())), NextPageToken: nextToken(pg)}, nil
}

// GetWork returns one title in full: the details, the external IDs and, for a series, its seasons.
func (p *plugin) GetWork(ctx context.Context, req *pluginv1.GetWorkRequest) (*pluginv1.Work, error) {
	tg, err := p.api.target()
	if err != nil {
		return nil, err
	}
	kind, id, ok := parseKey(req.GetKey())
	if !ok {
		return nil, sdk.NotFound("unknown work")
	}
	var r result
	if err := p.api.get(ctx, tg, "/"+kind+"/"+strconv.Itoa(id), url.Values{"language": {language(req.GetLanguages())}}, &r); err != nil {
		return nil, err
	}
	r.ID, r.MediaType = id, kind
	w := toWork(r, kind)
	if w == nil {
		return nil, sdk.NotFound("unknown work")
	}
	w.RuntimeMinutes = int32(r.Runtime)
	if kind == kindTV && len(r.EpisodeRun) > 0 {
		w.RuntimeMinutes = int32(r.EpisodeRun[0])
	}
	if r.ExternalIDs.IMDb != "" {
		w.ProviderIds["imdb"] = r.ExternalIDs.IMDb
	}
	if r.ExternalIDs.TVDB > 0 && kind == kindTV {
		w.ProviderIds["tvdb"] = strconv.Itoa(r.ExternalIDs.TVDB)
	}
	w.ContentRating = contentRating(r, kind)
	if kind == kindTV {
		t := messages.For(req.GetLanguages().GetLanguage())
		for _, s := range r.Seasons {
			if len(w.Seasons) >= pluginapi.MaxWorkSeasons {
				break
			}
			w.Seasons = append(w.Seasons, &pluginv1.WorkSeason{Number: int32(s.SeasonNumber), Name: seasonName(t, s.SeasonNumber, s.Name),
				EpisodeCount: int32(s.EpisodeCount), Year: year(s.AirDate)})
		}
	}
	details(w, r, kind, messages.For(req.GetLanguages().GetLanguage()))
	return w, nil
}

// details fills what only the detail page shows: the tagline, genres, companies, credits, the related page and the trailers.
func details(w *pluginv1.Work, r result, kind string, t i18n.Translator) {
	w.Badges, w.Caption = detailRelease(r, kind, t)
	w.Tagline = truncate(strings.TrimSpace(r.Tagline), pluginapi.MaxWorkTagline)
	for _, g := range r.Genres {
		if name := strings.TrimSpace(g.Name); g.ID > 0 && name != "" && len(w.Genres) < pluginapi.MaxWorkGenres {
			w.Genres = append(w.Genres, &pluginv1.WorkRefLink{Text: truncate(name, pluginapi.MaxWorkGenreName), Ref: genreRef(kind, g.ID)})
		}
	}
	// Networks first. Seerr's studio pages list movies only, so the production companies of a series are plain text.
	seen := map[string]bool{}
	company := func(n named, ref string) {
		name := strings.TrimSpace(n.Name)
		if name == "" || seen[name] || len(w.Companies) >= pluginapi.MaxWorkCompanies {
			return
		}
		seen[name] = true
		l := &pluginv1.WorkRefLink{Text: truncate(name, pluginapi.MaxWorkCompanyName)}
		if n.ID > 0 {
			l.Ref = ref
		}
		w.Companies = append(w.Companies, l)
	}
	for _, n := range r.Networks {
		company(n, idRef(refNetwork, n.ID))
	}
	for _, n := range r.ProductionCompanies {
		ref := ""
		if kind == kindMovie {
			ref = idRef(refStudio, n.ID)
		}
		company(n, ref)
	}
	w.Credits = credits(r, kind)
	w.RelatedRef = refRec + ":" + kind + ":" + strconv.Itoa(r.ID)
	for _, v := range r.RelatedVideos {
		if len(w.ExternalLinks) >= 2 {
			break
		}
		if v.Type != "Trailer" || !strings.HasPrefix(v.URL, "https://") {
			continue
		}
		label := truncate(strings.TrimSpace(v.Name), pluginapi.MaxWorkLinkLabel)
		if label == "" {
			label = t.T("trailer")
		}
		w.ExternalLinks = append(w.ExternalLinks, &pluginv1.ExternalLink{Url: v.URL, Label: label})
	}
}

// credits lists the people of a title, the ones that matter most first: the directors of a movie or the creators of a series, then the cast in
// the order TMDB gives.
func credits(r result, kind string) []*pluginv1.WorkCredit {
	var out []*pluginv1.WorkCredit
	seen := map[int]bool{}
	add := func(p person, k pluginv1.WorkCreditKind, role string) {
		name := strings.TrimSpace(p.Name)
		if name == "" || p.ID <= 0 || seen[p.ID] || len(out) >= pluginapi.MaxWorkCredits {
			return
		}
		seen[p.ID] = true
		c := &pluginv1.WorkCredit{Name: truncate(name, pluginapi.MaxWorkCreditText), Kind: k, Role: truncate(strings.TrimSpace(role), pluginapi.MaxWorkCreditText), Ref: idRef(refPerson, p.ID)}
		if pth := first(p.ProfilePath, p.ProfileRaw); pth != "" {
			c.ImageUrl = imageBase + profileSize + pth
		}
		out = append(out, c)
	}
	if kind == kindTV {
		for _, p := range r.CreatedBy {
			add(p, pluginv1.WorkCreditKind_WORK_CREDIT_KIND_CREATOR, "")
		}
	}
	directors := 0
	for _, p := range r.Credits.Crew {
		if p.Job == "Director" && directors < 3 {
			directors++
			add(p, pluginv1.WorkCreditKind_WORK_CREDIT_KIND_DIRECTOR, "")
		}
	}
	for _, p := range r.Credits.Cast {
		add(p, pluginv1.WorkCreditKind_WORK_CREDIT_KIND_ACTOR, p.Character)
	}
	return out
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// contentRating picks the United States certification, the one Seerr always has, else the first one.
func contentRating(r result, kind string) string {
	if kind == kindTV {
		first := ""
		for _, c := range r.ContentRatings.Results {
			if c.Rating == "" {
				continue
			}
			if c.Country == "US" {
				return c.Rating
			}
			if first == "" {
				first = c.Rating
			}
		}
		return first
	}
	first := ""
	for _, c := range r.Releases.Results {
		for _, d := range c.Dates {
			if d.Certification == "" {
				continue
			}
			if c.Country == "US" {
				return d.Certification
			}
			if first == "" {
				first = d.Certification
			}
		}
	}
	return first
}

// seasonName is Seerr's name for the season (in the request's language), else a generic one.
func seasonName(t i18n.Translator, number int, seerrName string) string {
	if n := truncate(strings.TrimSpace(seerrName), pluginapi.MaxWorkSeasonName); n != "" {
		return n
	}
	return genericSeason(t, number)
}

func genericSeason(t i18n.Translator, number int) string {
	if number == 0 {
		return t.T("specials")
	}
	return t.Tf("season", number)
}

// truncate cuts s to at most n characters.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// works converts the movies and series of a list; people and entries without a usable title are dropped. only limits the list to one kind
// (empty keeps both).
func works(rs []result, only string, t i18n.Translator) []*pluginv1.Work {
	out := make([]*pluginv1.Work, 0, len(rs))
	for _, r := range rs {
		kind := r.MediaType
		if kind == "" {
			kind = only // Seerr's lists of one type do not always say it
		}
		if kind != kindMovie && kind != kindTV || (only != "" && kind != only) {
			continue
		}
		if w := toWork(r, kind); w != nil {
			w.Badges = releaseBadges(r, kind, t)
			out = append(out, w)
		}
		if len(out) >= pluginapi.MaxWorksPerPage {
			break
		}
	}
	return out
}

func toWork(r result, kind string) *pluginv1.Work {
	title, orig, date := r.Title, r.OriginalTitle, r.ReleaseDate
	if kind == kindTV {
		title, orig, date = r.Name, r.OriginalName, r.FirstAirDate
	}
	title = strings.TrimSpace(title)
	if r.ID <= 0 || title == "" {
		return nil
	}
	w := &pluginv1.Work{
		Key: workKey(kind, r.ID), Type: itemType(kind), Title: title, OriginalTitle: strings.TrimSpace(orig), Year: year(date),
		Overview: r.Overview, ProviderIds: map[string]string{providerTMDB: strconv.Itoa(r.ID)},
	}
	if r.PosterPath != "" {
		w.PosterUrl = imageBase + posterSize + r.PosterPath
	}
	if r.BackdropPath != "" {
		w.BackdropUrl = imageBase + backdropSize + r.BackdropPath
	}
	return w
}

func parsePage(token string) (int, error) {
	if token == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(token)
	if err != nil || n < 1 || n > 1000 {
		return 0, sdk.InvalidArgument("bad page token")
	}
	return n, nil
}

func nextToken(pg page) string {
	if pg.Page >= 1 && pg.Page < pg.TotalPages {
		return strconv.Itoa(pg.Page + 1)
	}
	return ""
}

// awaitLookups waits for the lookups to end, at most budget, or until ctx ends.
func awaitLookups(ctx context.Context, waits []<-chan struct{}, budget time.Duration) {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for _, done := range waits {
		select {
		case <-done:
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// today is the date "not out yet" is judged against, in UTC; tests replace it.
var today = func() string { return time.Now().UTC().Format("2006-01-02") }

// releaseDate is the date of a movie's release or a series' first episode, as Seerr writes it.
func releaseDate(r result, kind string) string {
	if kind == kindTV {
		return r.FirstAirDate
	}
	return r.ReleaseDate
}

// notOut reports whether the date is a real date later than today. An empty or unreadable date is not judged.
func notOut(date string) bool {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return false
	}
	return date > today()
}

// The badge of a title that is not out yet: amber, top left. The text is at most 8 characters in every language.
const badgeUnreleased = "#F59E0B"

func unreleasedBadge(t i18n.Translator) []*pluginv1.WorkBadge {
	return []*pluginv1.WorkBadge{{Text: t.T("badgeUnreleased"), Color: badgeUnreleased, Corner: pluginv1.WorkBadgeCorner_WORK_BADGE_CORNER_TOP_LEFT}}
}

// releaseBadges is the badge of a list entry that is not out yet; none for a title that is out or has no date (a date is not guessed).
func releaseBadges(r result, kind string, t i18n.Translator) []*pluginv1.WorkBadge {
	if notOut(releaseDate(r, kind)) {
		return unreleasedBadge(t)
	}
	return nil
}

// detailRelease is the badge and the caption of a details page, which also knows the production status: a title with no date that Seerr says
// is planned or in production is not out either. The caption gives the date when there is one.
func detailRelease(r result, kind string, t i18n.Translator) ([]*pluginv1.WorkBadge, string) {
	date := releaseDate(r, kind)
	if notOut(date) {
		return unreleasedBadge(t), truncate(t.Tf("releasesOn", date), pluginapi.MaxWorkCaption)
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		switch strings.ToLower(strings.TrimSpace(r.Status)) {
		case "planned", "rumored", "in production", "post production":
			return unreleasedBadge(t), ""
		}
	}
	return nil, ""
}
