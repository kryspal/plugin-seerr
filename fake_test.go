package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSeerr stands in for a Seerr server: it records requests and answers from fixed data. It insists on the API key, as Seerr does.
type fakeSeerr struct {
	mu   sync.Mutex
	reqs []rec

	// details by "movie:603"; users are the answer to /user; postStatus and postBody answer POST /request.
	details    map[string]string
	users      string
	postStatus int
	postBody   string
	// list, when set, replaces listJSON as the answer of the list endpoints (discover, trending).
	list string
	// sliderFails is how many calls to the genre slider fail with 500 before it answers; sliderEmpty gives genres with no backdrops.
	sliderFails int
	sliderEmpty bool
	// sliderDelay slows the genre slider, as a Seerr that asks TMDB for every genre.
	sliderDelay time.Duration
	// trending, when set, is the answer of /discover/trending.
	trending string
	// collections is the answer of /collection/{id} by ID.
	collections map[string]string
	// genres, when set, replaces the genre list.
	genres string
	// deleted is a Seerr user ID that no longer exists: a call made for it (X-API-User) is refused with 403, as Seerr does.
	deleted string
	// credits, when set, replaces the credits of person 6384.
	credits string
	// requests answers GET /request.
	requests string
	// lookupDelay slows the details asked in a language, as a cold Seerr is.
	lookupDelay time.Duration
}

type rec struct {
	method, path, query, user, body string
}

func (f *fakeSeerr) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, rec{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("X-API-User"), string(b)})
		f.mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "Kryspal Media v") {
			t.Errorf("missing User-Agent on %s", r.URL.Path)
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		if path == "/status" {
			_, _ = w.Write([]byte(`{"version":"3.5.1"}`))
			return
		}
		if r.Header.Get("X-Api-Key") != "k" {
			http.Error(w, `{"message":"no"}`, http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if f.deleted != "" && r.Header.Get("X-API-User") == f.deleted {
			http.Error(w, `{"message":"Forbidden"}`, http.StatusForbidden)
			return
		}
		switch {
		case path == "/auth/me":
			_, _ = w.Write([]byte(`{"id":1}`))
		case path == "/user":
			_, _ = w.Write([]byte(f.users))
		case path == "/genres/movie", path == "/genres/tv":
			if f.genres != "" {
				_, _ = w.Write([]byte(f.genres))
				return
			}
			_, _ = w.Write([]byte(`[{"id":28,"name":"动作"},{"id":878,"name":"科幻"},{"id":35,"name":"Comedy"}]`))
		case path == "/discover/genreslider/movie", path == "/discover/genreslider/tv":
			f.mu.Lock()
			fail := f.sliderFails > 0
			if fail {
				f.sliderFails--
			}
			empty := f.sliderEmpty
			delay := f.sliderDelay
			f.mu.Unlock()
			time.Sleep(delay)
			if fail {
				http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
				return
			}
			if empty {
				_, _ = w.Write([]byte(`[{"id":28,"name":"动作","backdrops":[]},{"id":35,"name":"Comedy","backdrops":[]}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"id":28,"name":"动作","backdrops":["/g28.jpg","/x.jpg"]},{"id":35,"name":"Comedy","backdrops":[]}]`))
		case path == "/studio/79":
			_, _ = w.Write([]byte(`{"id":79,"name":"Village Roadshow Pictures","description":"A studio","headquarters":"Burbank, CA","originCountry":"US","logoPath":"/s79.png"}`))
		case path == "/network/174":
			_, _ = w.Write([]byte(`{"id":174,"name":"AMC","originCountry":"US","logoPath":"/n174.png"}`))
		case path == "/person/6384":
			_, _ = w.Write([]byte(`{"id":6384,"name":"Keanu","biography":"Bio","birthday":"1964-09-02","deathday":null,"placeOfBirth":"Beirut","profilePath":"/k.jpg"}`))
		case path == "/person/6384/combined_credits":
			_, _ = w.Write([]byte(f.personCredits()))
		case path == "/person/7/combined_credits":
			_, _ = w.Write([]byte(`{"cast":[],"crew":[
			 {"id":603,"mediaType":"movie","title":"Movie A","releaseDate":"1999-03-30","job":"Producer"},
			 {"id":603,"mediaType":"movie","title":"Movie A","releaseDate":"1999-03-30","job":"Director"},
			 {"id":1396,"mediaType":"tv","name":"Series B","job":"Writer"}]}`))
		case path == "/person/7":
			_, _ = w.Write([]byte(`{"id":7,"name":"Maker"}`))
		case strings.HasPrefix(path, "/person/"):
			http.Error(w, `{"message":"Unable to retrieve person."}`, http.StatusInternalServerError)
		case strings.HasSuffix(path, "/recommendations"):
			if f.list != "" {
				_, _ = w.Write([]byte(f.list))
				return
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			_, _ = w.Write([]byte(f.listOf(strings.HasPrefix(path, "/tv/"), max(page, 1))))
		case strings.HasPrefix(path, "/collection/"):
			if body, ok := f.collections[strings.TrimPrefix(path, "/collection/")]; ok {
				_, _ = w.Write([]byte(body))
				return
			}
			http.NotFound(w, r)
		case path == "/request" && r.Method == http.MethodGet:
			if f.requests == "" {
				_, _ = w.Write([]byte(`{"pageInfo":{"pages":0,"pageSize":20,"results":0,"page":1},"results":[]}`))
				return
			}
			_, _ = w.Write([]byte(f.requests))
		case path == "/request" && r.Method == http.MethodPost:
			w.WriteHeader(f.postStatus)
			_, _ = w.Write([]byte(f.postBody))
		case path == "/search":
			if strings.Contains(r.URL.RawQuery, "+") { // Seerr answers 400 "query must be url encoded"
				http.Error(w, `{"message":"query must be url encoded"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(searchJSON))
		case path == "/discover/trending" && f.trending != "":
			_, _ = w.Write([]byte(f.trending))
		case (strings.HasPrefix(path, "/discover/movies") || strings.HasPrefix(path, "/discover/tv")) && f.list == "":
			// Distinct titles per list and page, so that a page does not repeat a title by accident.
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page < 1 {
				page = 1
			}
			_, _ = w.Write([]byte(f.listOf(strings.HasPrefix(path, "/discover/tv"), page)))
		case strings.HasPrefix(path, "/discover/movies"), strings.HasPrefix(path, "/discover/tv"):
			// Seerr v3.5.1 reads language here as an original-language filter: a tag with a region matches nothing.
			if l := r.URL.Query().Get("language"); l != "" && l != "all" && strings.Contains(l, "-") {
				_, _ = w.Write([]byte(`{"page":1,"totalPages":1,"totalResults":0,"results":[]}`))
				return
			}
			_, _ = w.Write([]byte(f.listBody()))
		case strings.HasPrefix(path, "/discover/"):
			_, _ = w.Write([]byte(f.listBody()))
		case strings.HasPrefix(path, "/movie/"), strings.HasPrefix(path, "/tv/"):
			if f.lookupDelay > 0 && r.URL.Query().Get("language") != "" {
				time.Sleep(f.lookupDelay)
			}
			key := strings.Replace(strings.TrimPrefix(path, "/"), "/", ":", 1)
			d, ok := f.details[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(d))
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeSeerr) last(t *testing.T) rec {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("no request reached Seerr")
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeSeerr) posts() []rec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rec
	for _, r := range f.reqs {
		if r.method == http.MethodPost {
			out = append(out, r)
		}
	}
	return out
}

const listJSON = `{"page":1,"totalPages":3,"totalResults":60,"results":[
 {"id":603,"mediaType":"movie","title":"Movie A","originalTitle":"Movie A orig","releaseDate":"1999-03-30","overview":"About A","posterPath":"/p.jpg","backdropPath":"/b.jpg"},
 {"id":1396,"mediaType":"tv","name":"Series B","originalName":"Series B orig","firstAirDate":"2008-01-20","posterPath":"/q.jpg"},
 {"id":99,"mediaType":"person","name":"Someone"},
 {"id":5,"mediaType":"movie","title":""}
]}`

const searchJSON = `{"page":2,"totalPages":2,"totalResults":30,"results":[
 {"id":603,"mediaType":"movie","title":"Movie A","releaseDate":"1999-03-30"},
 {"id":1396,"mediaType":"tv","name":"Series B","firstAirDate":"2008-01-20"}
]}`

// newTestPlugin returns a plugin pointed at a fake Seerr that has the key "k" and no cache delay.
func newTestPlugin(t *testing.T) (*plugin, *fakeSeerr) {
	t.Helper()
	f := &fakeSeerr{details: map[string]string{}, postStatus: http.StatusCreated, postBody: `{"id":7,"status":1}`,
		users: `{"results":[{"id":5,"username":"mia","displayName":"Mia","jellyfinUsername":"mia"},{"id":6,"username":"bob","jellyfinUsername":"bob"}]}`}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	p := newPlugin()
	p.api.set(srv.URL, "k", "")
	return p, f
}

// count is the number of requests whose path starts with prefix.
func (f *fakeSeerr) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if strings.HasPrefix(r.path, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeSeerr) listBody() string {
	if f.list != "" {
		return f.list
	}
	return listJSON
}

// personCredits is the answer for person 6384: Seerr's order is not by date, and it lists talk-show appearances.
func (f *fakeSeerr) personCredits() string {
	if f.credits != "" {
		return f.credits
	}
	return `{"id":6384,"cast":[
	 {"id":10,"mediaType":"movie","title":"Old","releaseDate":"1991-02-01","character":"Ted","genreIds":[35]},
	 {"id":11,"mediaType":"movie","title":"Undated","character":"Nobody","genreIds":[]},
	 {"id":12,"mediaType":"tv","name":"Late Show","firstAirDate":"2003-01-26","character":"Self","genreIds":[10767],"episodeCount":3},
	 {"id":13,"mediaType":"movie","title":"New","releaseDate":"2021-12-22","character":"Neo","genreIds":[878],"mediaInfo":{"status":5}},
	 {"id":14,"mediaType":"tv","name":"Show","firstAirDate":"2015-01-01","character":"Himself","genreIds":[18]},
	 {"id":99,"mediaType":"person","name":"x"}
	],"crew":[{"id":15,"mediaType":"movie","title":"Made","releaseDate":"2000-01-01","job":"Producer"}]}`
}

// listOf is a list of 20 titles of one type, numbered by page: 20 titles on page 1 have IDs 1001 to 1020 (movies) or 2001 to 2020 (series),
// page 2 the next 20 IDs, three pages in all.
func (f *fakeSeerr) listOf(tv bool, page int) string {
	base, kind, field := 1000, "movie", "title"
	if tv {
		base, kind, field = 2000, "tv", "name"
	}
	var rs []string
	for i := 1; i <= 20; i++ {
		id := base + (page-1)*20 + i
		rs = append(rs, `{"id":`+strconv.Itoa(id)+`,"mediaType":"`+kind+`","`+field+`":"T`+strconv.Itoa(id)+`","posterPath":"/p`+strconv.Itoa(id)+`.jpg"}`)
	}
	return `{"page":` + strconv.Itoa(page) + `,"totalPages":3,"totalResults":60,"results":[` + strings.Join(rs, ",") + `]}`
}

// countExact is the number of requests for exactly this path.
func (f *fakeSeerr) countExact(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.path == path {
			n++
		}
	}
	return n
}
