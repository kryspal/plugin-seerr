package main

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A person's page: the header from /person/{id} (in the member's language, like the details of a title) and the works from
// /person/{id}/combined_credits, which Seerr gives whole and in its own language, without paging. The plugin pages them itself.

const (
	creditsPageSize = 20
	creditsTTL      = 5 * time.Minute
	creditsCacheMax = 200

	// TMDB genres of television that are appearances rather than work: talk shows and news.
	genreTalk = 10767
	genreNews = 10763
)

// credit is one work in a person's list, ready to show.
type credit struct {
	r       result
	caption string
}

type creditsEntry struct {
	at   time.Time
	list []credit
}

// creditCache keeps the sorted works of a person for five minutes, so paging does not ask Seerr again.
type creditCache struct {
	mu  sync.Mutex
	m   map[string]creditsEntry
	now func() time.Time
}

func newCreditCache() *creditCache {
	return &creditCache{m: map[string]creditsEntry{}, now: time.Now}
}

func (c *creditCache) get(key string) ([]credit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || c.now().Sub(e.at) > creditsTTL {
		return nil, false
	}
	return e.list, true
}

func (c *creditCache) put(key string, list []credit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= creditsCacheMax {
		c.m = map[string]creditsEntry{}
	}
	c.m[key] = creditsEntry{at: c.now(), list: list}
}

func (c *creditCache) clear() {
	c.mu.Lock()
	c.m = map[string]creditsEntry{}
	c.mu.Unlock()
}

// personPage builds the page of a person.
func (pc *pageCtx) personPage(id int) *pageDef {
	def := &pageDef{header: func(ctx context.Context) (*pluginv1.DiscoverPageHeader, error) { return pc.personHeader(ctx, id) }}
	def.rows = []rowSpec{{id: "credits", fetch: func(ctx context.Context, pageNo int) (*pluginv1.DiscoverRow, error) {
		list, err := pc.p.credits(ctx, pc.tg, id)
		if err != nil {
			return nil, err
		}
		from := (pageNo - 1) * creditsPageSize
		if from >= len(list) {
			return &pluginv1.DiscoverRow{Id: "credits", Title: pc.t.T("rowCredits")}, nil
		}
		to := min(from+creditsPageSize, len(list))
		ws := make([]*pluginv1.Work, 0, to-from)
		for _, c := range list[from:to] {
			if w := toWork(c.r, c.r.MediaType); w != nil {
				w.Caption = truncate(c.caption, 64)
				w.Badges = releaseBadges(c.r, c.r.MediaType, pc.t)
				ws = append(ws, w)
			}
		}
		row := &pluginv1.DiscoverRow{Id: "credits", Title: pc.t.T("rowCredits"), Works: ws}
		if to < len(list) {
			row.NextPageToken = strconv.Itoa(pageNo + 1)
		}
		return row, nil
	}}}
	return def
}

func (pc *pageCtx) personHeader(ctx context.Context, id int) (*pluginv1.DiscoverPageHeader, error) {
	var d struct {
		Name       string `json:"name"`
		Biography  string `json:"biography"`
		Birthday   string `json:"birthday"`
		Deathday   string `json:"deathday"`
		BirthPlace string `json:"placeOfBirth"`
		ProfilePth string `json:"profilePath"`
	}
	if err := pc.p.api.get(ctx, pc.tg, "/person/"+strconv.Itoa(id), url.Values{"language": {tmdbLanguage(pc.tag)}}, &d); err != nil {
		return nil, personError(err)
	}
	name := strings.TrimSpace(d.Name)
	if name == "" {
		return nil, sdk.NotFound("unknown person")
	}
	h := &pluginv1.DiscoverPageHeader{Title: truncate(name, pluginMaxHeader), Overview: truncate(strings.TrimSpace(d.Biography), 8000)}
	if d.ProfilePth != "" {
		h.ImageUrl = imageBase + profileSize + d.ProfilePth
		h.ImageKind = pluginv1.DiscoverCardImageKind_DISCOVER_CARD_IMAGE_KIND_PORTRAIT
	}
	h.Facts = facts(fact{pc.t.T("factBorn"), d.Birthday}, fact{pc.t.T("factDied"), d.Deathday}, fact{pc.t.T("factBirthplace"), d.BirthPlace})
	return h, nil
}

// credits returns the works of a person, newest first: what the person acted in, or, for someone with no acting, what they made. Seerr's
// list is not ordered by date and has no paging.
func (p *plugin) credits(ctx context.Context, tg target, id int) ([]credit, error) {
	key := tg.base + "#" + strconv.Itoa(id)
	if l, ok := p.people.get(key); ok {
		return l, nil
	}
	var c struct {
		Cast []result `json:"cast"`
		Crew []result `json:"crew"`
	}
	if err := p.api.get(ctx, tg, "/person/"+strconv.Itoa(id)+"/combined_credits", nil, &c); err != nil {
		return nil, personError(err)
	}
	list := castCredits(c.Cast)
	if len(list) == 0 {
		list = crewCredits(c.Crew)
	}
	sortCredits(list)
	p.people.put(key, list)
	var rs []result
	for _, cr := range list {
		rs = append(rs, cr.r)
	}
	p.states.record(rs)
	return list, nil
}

// personError turns the failure for a person Seerr cannot find into NOT_FOUND. Seerr v3.5.1 answers 500 "Unable to retrieve person" for a
// person TMDB does not have, and the HTTP client drops the body of a 5xx answer, so the status is all there is to go by: a 500 for a person
// is read as unknown. Other failures (Seerr down, refused key) keep their meaning.
func personError(err error) error {
	if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable && strings.Contains(st.Message(), "500 Internal Server Error") {
		return sdk.NotFound("unknown person")
	}
	return err
}

func isWork(r result) bool { return r.ID > 0 && (r.MediaType == kindMovie || r.MediaType == kindTV) }

// castCredits keeps the movies and series a person acted in. Appearances on talk shows and news are left out, judged by the genres Seerr
// gives, unless that would leave nothing.
func castCredits(cast []result) []credit {
	var all, kept []credit
	for _, r := range cast {
		if !isWork(r) {
			continue
		}
		c := credit{r: r, caption: strings.TrimSpace(r.Character)}
		all = append(all, c)
		if r.MediaType == kindTV && hasGenre(r.GenreIDs, genreTalk, genreNews) {
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		return all
	}
	return kept
}

func hasGenre(have []int, want ...int) bool {
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}

// crewCredits lists what a person made, one entry per work with the jobs joined.
func crewCredits(crew []result) []credit {
	var out []credit
	at := map[string]int{}
	for _, r := range crew {
		if !isWork(r) {
			continue
		}
		k := r.MediaType + ":" + strconv.Itoa(r.ID)
		job := strings.TrimSpace(r.Job)
		if i, ok := at[k]; ok {
			if job != "" && !strings.Contains(", "+out[i].caption+", ", ", "+job+", ") {
				out[i].caption = strings.Trim(out[i].caption+", "+job, ", ")
			}
			continue
		}
		at[k] = len(out)
		out = append(out, credit{r: r, caption: job})
	}
	return out
}

// sortCredits orders by date, newest first; works with no date go last. The order of equal dates is kept.
func sortCredits(list []credit) {
	date := func(c credit) string {
		if c.r.MediaType == kindTV {
			return c.r.FirstAirDate
		}
		return c.r.ReleaseDate
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := date(list[i]), date(list[j])
		switch {
		case a == "":
			return false
		case b == "":
			return true
		}
		return a > b
	})
}
