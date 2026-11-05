package main

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kryspal/plugin-sdk/i18n"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
)

// The pages of the source, named by refs:
//
//	home | movies | series | genres        the four tabs
//	genre:<movie|tv>:<TMDB genre ID>       the titles of one genre
//	studio:<TMDB company ID>               the movies of one studio
//	network:<TMDB network ID>              the series of one network
//	person:<TMDB person ID>                the works of one person
//	rec:<movie|tv>:<TMDB ID>               what Seerr recommends after a title (the detail page's related_ref)
//
// The lists of every page show the text Seerr gives (its own language setting), so a page has one language. The labels, the filters and the
// details of a title follow the member's language.
const (
	refHome   = "home"
	refMovies = "movies"
	refSeries = "series"
	refGenres = "genres"

	refGenre   = "genre"
	refStudio  = "studio"
	refNetwork = "network"
	refPerson  = "person"
	refRec     = "rec"
)

// pref is a parsed ref.
type pref struct {
	kind  string // one of the constants above
	media string // movie or tv, for genre and rec
	id    int
}

func parseRef(ref string) (pref, bool) {
	switch ref {
	case refHome, refMovies, refSeries, refGenres:
		return pref{kind: ref}, true
	}
	parts := strings.Split(ref, ":")
	switch {
	case len(parts) == 3 && (parts[0] == refGenre || parts[0] == refRec) && (parts[1] == kindMovie || parts[1] == kindTV):
		if id, ok := positive(parts[2]); ok {
			return pref{kind: parts[0], media: parts[1], id: id}, true
		}
	case len(parts) == 2 && (parts[0] == refStudio || parts[0] == refNetwork || parts[0] == refPerson):
		if id, ok := positive(parts[1]); ok {
			return pref{kind: parts[0], id: id}, true
		}
	}
	return pref{}, false
}

func positive(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0 && strconv.Itoa(n) == s
}

func genreRef(media string, id int) string { return refGenre + ":" + media + ":" + strconv.Itoa(id) }
func idRef(kind string, id int) string     { return kind + ":" + strconv.Itoa(id) }

// DescribeDiscoverSource declares the tabs and the types search can filter by.
func (p *plugin) DescribeDiscoverSource(_ context.Context, req *pluginv1.DescribeDiscoverSourceRequest) (*pluginv1.DiscoverSourceInfo, error) {
	if _, err := p.api.target(); err != nil {
		return nil, err
	}
	t := messages.For(req.GetLanguages().GetLanguage())
	return &pluginv1.DiscoverSourceInfo{
		Tabs: []*pluginv1.DiscoverTab{
			{Title: t.T("tabHome"), Ref: refHome},
			{Title: t.T("tabMovies"), Ref: refMovies},
			{Title: t.T("tabSeries"), Ref: refSeries},
			{Title: t.T("tabGenres"), Ref: refGenres},
		},
		SearchTypes: []pluginv1.ItemType{pluginv1.ItemType_ITEM_TYPE_MOVIE, pluginv1.ItemType_ITEM_TYPE_SERIES},
	}, nil
}

// A page is built from a header, the controls and rows; each row fetches itself, so one row can be asked for alone.
type pageDef struct {
	header  func(ctx context.Context) (*pluginv1.DiscoverPageHeader, error)
	filters []*pluginv1.WorkOption
	sort    *pluginv1.WorkOption
	rows    []rowSpec
}

type rowSpec struct {
	id    string
	fetch func(ctx context.Context, pageNo int) (*pluginv1.DiscoverRow, error)
}

// pageCtx is what the builders of one request share.
type pageCtx struct {
	p   *plugin
	tg  target
	t   i18n.Translator
	tag string
	sel map[string][]string
}

// GetDiscoverPage returns one page, or one row of it.
func (p *plugin) GetDiscoverPage(ctx context.Context, req *pluginv1.GetDiscoverPageRequest) (*pluginv1.DiscoverPage, error) {
	tg, err := p.api.target()
	if err != nil {
		return nil, err
	}
	ref, ok := parseRef(req.GetRef())
	if !ok {
		return nil, sdk.NotFound("unknown page")
	}
	pageNo := 1
	if req.GetRowId() != "" {
		if pageNo, err = parsePage(req.GetPageToken()); err != nil {
			return nil, err
		}
	}
	tag := req.GetLanguages().GetLanguage()
	pc := &pageCtx{p: p, tg: tg, t: messages.For(tag), tag: tag, sel: selected(req.GetSelections())}
	var def *pageDef
	switch ref.kind {
	case refHome:
		def = pc.home(ctx)
	case refMovies:
		def, err = pc.listing(ctx, kindMovie)
	case refSeries:
		def, err = pc.listing(ctx, kindTV)
	case refGenres:
		def = pc.genreWalls()
	case refGenre:
		def, err = pc.genrePage(ctx, ref.media, ref.id)
	case refStudio:
		def = pc.companyPage(refStudio, ref.id)
	case refNetwork:
		def = pc.companyPage(refNetwork, ref.id)
	case refPerson:
		def = pc.personPage(ref.id)
	case refRec:
		def = pc.recommendations(ref.media, ref.id)
	}
	if err != nil {
		return nil, err
	}
	return p.serve(ctx, def, req.GetRowId(), pageNo)
}

// serve answers for a page definition: the whole page, or the row named by rowID from pageNo.
func (p *plugin) serve(ctx context.Context, def *pageDef, rowID string, pageNo int) (*pluginv1.DiscoverPage, error) {
	out := &pluginv1.DiscoverPage{Filters: def.filters, Sort: def.sort}
	if rowID != "" {
		for _, r := range def.rows {
			if r.id != rowID {
				continue
			}
			// The header comes with a row asked alone, so the page can say where the row is ("Person > Works"). It is fetched with the row.
			var herr error
			var wg sync.WaitGroup
			if def.header != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					out.Header, herr = def.header(ctx)
				}()
			}
			row, err := r.fetch(ctx, pageNo)
			wg.Wait()
			if err != nil {
				return nil, err
			}
			if herr != nil {
				return nil, herr
			}
			out.Rows = []*pluginv1.DiscoverRow{row}
			return out, nil
		}
		return nil, sdk.NotFound("unknown row")
	}
	rows := make([]*pluginv1.DiscoverRow, len(def.rows))
	errs := make([]error, len(def.rows))
	var herr error
	var wg sync.WaitGroup
	if def.header != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out.Header, herr = def.header(ctx)
		}()
	}
	for i, r := range def.rows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i], errs[i] = r.fetch(ctx, 1)
		}()
	}
	wg.Wait()
	if herr != nil {
		return nil, herr
	}
	var firstErr error
	for i, r := range rows {
		switch {
		case errs[i] != nil && ctx.Err() != nil:
			slog.Debug("discover row cancelled", "row", def.rows[i].id)
		case errs[i] != nil:
			slog.Warn("discover row failed", "row", def.rows[i].id, "error", errs[i].Error())
			if firstErr == nil {
				firstErr = errs[i]
			}
		case r != nil && (len(r.Works) > 0 || len(r.Cards) > 0 || r.NextPageToken != ""):
			out.Rows = append(out.Rows, r)
		case ctx.Err() == nil:
			slog.Debug("discover row is empty", "row", def.rows[i].id)
		}
	}
	if len(out.Rows) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// ---- rows ----

// listRow is a row of one Seerr list: the works of path with q and the page, as Seerr gives them.
func (pc *pageCtx) listRow(id, title, path string, q url.Values, media string, style pluginv1.DiscoverRowStyle, more string) rowSpec {
	return rowSpec{id: id, fetch: func(ctx context.Context, pageNo int) (*pluginv1.DiscoverRow, error) {
		qq := url.Values{"page": {strconv.Itoa(pageNo)}}
		for k, v := range q {
			qq[k] = v
		}
		pg, err := pc.p.page(ctx, pc.tg, path, qq)
		if err != nil {
			return nil, err
		}
		return &pluginv1.DiscoverRow{Id: id, Title: title, Style: style, Works: works(pg.Results, media), NextPageToken: nextToken(pg), MoreRef: more}, nil
	}}
}

// filteredRow is a listRow of /discover/movies or /discover/tv with the member's choices; the title follows the sort.
func (pc *pageCtx) filteredRow(id, popularKey, media string, extra url.Values, more string) rowSpec {
	path := "/discover/movies"
	if media == kindTV {
		path = "/discover/tv"
	}
	q := url.Values{}
	sortValue := applyFilters(q, media, pc.sel, time.Now().UTC().Format("2006-01-02"))
	for k, v := range extra {
		q[k] = v
	}
	title := pc.t.T(popularKey)
	if sortValue != defaultSort {
		title = pc.t.T(map[string]string{"newest": "rowNewest", "top_rated": "rowTop"}[sortValue] + map[string]string{kindMovie: "Movies", kindTV: "Series"}[media])
	}
	return pc.listRow(id, title, path, q, media, pluginv1.DiscoverRowStyle_DISCOVER_ROW_STYLE_POSTERS, more)
}

func popularKey(media string) string {
	if media == kindTV {
		return "rowPopularSeries"
	}
	return "rowPopularMovies"
}

// cardsRow is a wall of entry cards.
func cardsRow(id, title string, cards []*pluginv1.DiscoverCard) rowSpec {
	return rowSpec{id: id, fetch: func(context.Context, int) (*pluginv1.DiscoverRow, error) {
		return &pluginv1.DiscoverRow{Id: id, Title: title, Style: pluginv1.DiscoverRowStyle_DISCOVER_ROW_STYLE_CARDS, Cards: cards}, nil
	}}
}

// ---- the tabs ----

// home is a hero of what is trending, the trending list after it, the popular lists and the studio and network walls.
func (pc *pageCtx) home(ctx context.Context) *pageDef {
	// The hero and the trending row come from the same Seerr page; it is fetched once per request.
	first := sync.OnceValues(func() (page, error) { return pc.trending(ctx, 1) })
	hero := func(r result) bool {
		return r.BackdropPath != "" && (r.MediaType == kindMovie || r.MediaType == kindTV)
	}
	def := &pageDef{}
	def.rows = append(def.rows,
		rowSpec{id: "hero", fetch: func(ctx context.Context, _ int) (*pluginv1.DiscoverRow, error) {
			pg, err := first()
			if err != nil {
				return nil, err
			}
			var rs []result
			for _, r := range pg.Results {
				if hero(r) && len(rs) < 10 {
					rs = append(rs, r)
				}
			}
			return &pluginv1.DiscoverRow{Id: "hero", Style: pluginv1.DiscoverRowStyle_DISCOVER_ROW_STYLE_HERO, Works: works(rs, "")}, nil
		}},
		rowSpec{id: "trending", fetch: func(ctx context.Context, pageNo int) (*pluginv1.DiscoverRow, error) {
			var pg page
			var err error
			if pageNo == 1 {
				pg, err = first()
			} else {
				pg, err = pc.trending(ctx, pageNo)
			}
			if err != nil {
				return nil, err
			}
			rs := pg.Results
			if pageNo == 1 { // the hero shows the first ten with a picture
				shown := 0
				rs = nil
				for _, r := range pg.Results {
					if hero(r) && shown < 10 {
						shown++
						continue
					}
					rs = append(rs, r)
				}
			}
			return &pluginv1.DiscoverRow{Id: "trending", Title: pc.t.T("rowTrending"), Works: works(rs, ""), NextPageToken: nextToken(pg)}, nil
		}},
	)
	nosel := &pageCtx{p: pc.p, tg: pc.tg, t: pc.t, tag: pc.tag}
	def.rows = append(def.rows,
		nosel.filteredRow("popular_movies", "rowPopularMovies", kindMovie, nil, refMovies),
		nosel.filteredRow("popular_series", "rowPopularSeries", kindTV, nil, refSeries),
		cardsRow("studios", pc.t.T("rowStudios"), wallCards(studioWall, refStudio)),
		cardsRow("networks", pc.t.T("rowNetworks"), wallCards(networkWall, refNetwork)),
	)
	return def
}

func (pc *pageCtx) trending(ctx context.Context, pageNo int) (page, error) {
	return pc.p.page(ctx, pc.tg, "/discover/trending", url.Values{"page": {strconv.Itoa(pageNo)}, "mediaType": {"all"}})
}

func wallCards(entries []wallEntry, kind string) []*pluginv1.DiscoverCard {
	out := make([]*pluginv1.DiscoverCard, 0, len(entries))
	for _, e := range entries {
		// A logo is shown whole, not cropped to the card; w500 keeps the logo's own shape without enlarging a narrow picture.
		c := &pluginv1.DiscoverCard{Title: e.name, Ref: idRef(kind, e.id), ImageKind: pluginv1.DiscoverCardImageKind_DISCOVER_CARD_IMAGE_KIND_LOGO}
		if e.logo != "" {
			c.ImageUrl = imageBase + cardLogoSize + e.logo
		}
		out = append(out, c)
	}
	return out
}

// listing is the Movies or Series tab: the popular list with the filters and the sort.
func (pc *pageCtx) listing(ctx context.Context, media string) (*pageDef, error) {
	genres, err := pc.p.genres.list(ctx, pc.p, pc.tg, media, pc.tag)
	if err != nil {
		return nil, sdk.Unavailable("seerr: the genre list is not available")
	}
	def := &pageDef{}
	def.filters, def.sort = filterOptions(pc.t, genres)
	def.rows = []rowSpec{pc.filteredRow(map[string]string{kindMovie: "popular_movies", kindTV: "popular_series"}[media], popularKey(media), media, nil, "")}
	return def, nil
}

// genreWalls is the Genres tab: one wall of cards for movies and one for series.
func (pc *pageCtx) genreWalls() *pageDef {
	wall := func(id, titleKey, media string) rowSpec {
		return rowSpec{id: id, fetch: func(ctx context.Context, _ int) (*pluginv1.DiscoverRow, error) {
			cards, err := pc.p.genres.wall(ctx, pc.p, pc.tg, media, pc.tag)
			if err != nil {
				return nil, err
			}
			out := make([]*pluginv1.DiscoverCard, 0, len(cards))
			for _, c := range cards {
				out = append(out, &pluginv1.DiscoverCard{Title: truncate(c.Name, 64), ImageUrl: c.Backdrop, Ref: genreRef(media, c.ID), ImageKind: pluginv1.DiscoverCardImageKind_DISCOVER_CARD_IMAGE_KIND_COVER})
			}
			return &pluginv1.DiscoverRow{Id: id, Title: pc.t.T(titleKey), Style: pluginv1.DiscoverRowStyle_DISCOVER_ROW_STYLE_CARDS, Cards: out}, nil
		}}
	}
	return &pageDef{rows: []rowSpec{wall("genres_movies", "rowGenresMovies", kindMovie), wall("genres_series", "rowGenresSeries", kindTV)}}
}

// ---- genre, studio, network ----

func (pc *pageCtx) genrePage(ctx context.Context, media string, id int) (*pageDef, error) {
	genres, err := pc.p.genres.list(ctx, pc.p, pc.tg, media, pc.tag)
	if err != nil {
		return nil, sdk.Unavailable("seerr: the genre list is not available")
	}
	name := ""
	for _, g := range genres {
		if g.ID == id {
			name = g.Name
		}
	}
	if name == "" {
		return nil, sdk.NotFound("unknown genre")
	}
	def := &pageDef{header: func(context.Context) (*pluginv1.DiscoverPageHeader, error) {
		return &pluginv1.DiscoverPageHeader{Title: truncate(name, pluginMaxHeader), Subtitle: pc.t.T(map[string]string{kindMovie: "kindMovies", kindTV: "kindSeries"}[media])}, nil
	}}
	def.filters, def.sort = filterOptions(pc.t, nil)
	def.rows = []rowSpec{pc.filteredRow("works", popularKey(media), media, url.Values{"genre": {strconv.Itoa(id)}}, "")}
	return def, nil
}

// companyPage is the page of a studio (movies) or a network (series).
func (pc *pageCtx) companyPage(kind string, id int) *pageDef {
	media, path, param := kindMovie, "/studio/", "studio"
	if kind == refNetwork {
		media, path, param = kindTV, "/network/", "network"
	}
	def := &pageDef{}
	def.filters, def.sort = filterOptions(pc.t, nil)
	def.header = func(ctx context.Context) (*pluginv1.DiscoverPageHeader, error) {
		var c struct {
			Name         string `json:"name"`
			Description  string `json:"description"`
			Headquarters string `json:"headquarters"`
			OriginCtry   string `json:"originCountry"`
			LogoPath     string `json:"logoPath"`
		}
		if err := pc.p.api.get(ctx, pc.tg, path+strconv.Itoa(id), nil, &c); err != nil {
			return nil, err
		}
		h := &pluginv1.DiscoverPageHeader{Title: truncate(strings.TrimSpace(c.Name), pluginMaxHeader), Overview: truncate(c.Description, 8000)}
		if c.LogoPath != "" {
			h.ImageUrl = imageBase + logoSize + c.LogoPath
		}
		h.Facts = facts(fact{pc.t.T("factHQ"), c.Headquarters}, fact{pc.t.T("factCountry"), c.OriginCtry})
		return h, nil
	}
	def.rows = []rowSpec{pc.filteredRow("works", popularKey(media), media, url.Values{param: {strconv.Itoa(id)}}, "")}
	return def
}

type fact struct{ label, value string }

// facts keeps the pairs that have a value.
func facts(fs ...fact) []*pluginv1.DiscoverFact {
	var out []*pluginv1.DiscoverFact
	for _, f := range fs {
		if v := strings.TrimSpace(f.value); v != "" {
			out = append(out, &pluginv1.DiscoverFact{Label: f.label, Value: truncate(v, 128)})
		}
	}
	return out
}

const pluginMaxHeader = 128

// ---- recommendations ----

// recommendations is the related page of a title: what Seerr recommends after it.
func (pc *pageCtx) recommendations(media string, id int) *pageDef {
	path := "/" + media + "/" + strconv.Itoa(id) + "/recommendations"
	return &pageDef{rows: []rowSpec{pc.listRow("recommended", pc.t.T("rowRecommended"), path, nil, media, pluginv1.DiscoverRowStyle_DISCOVER_ROW_STYLE_POSTERS, "")}}
}
