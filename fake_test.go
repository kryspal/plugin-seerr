package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
		switch {
		case path == "/auth/me":
			_, _ = w.Write([]byte(`{"id":1}`))
		case path == "/user":
			_, _ = w.Write([]byte(f.users))
		case path == "/request" && r.Method == http.MethodPost:
			w.WriteHeader(f.postStatus)
			_, _ = w.Write([]byte(f.postBody))
		case path == "/search":
			if strings.Contains(r.URL.RawQuery, "+") { // Seerr answers 400 "query must be url encoded"
				http.Error(w, `{"message":"query must be url encoded"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(searchJSON))
		case path == "/settings/main":
			_, _ = w.Write([]byte(`{"locale":"en"}`))
		case strings.HasPrefix(path, "/discover/movies"), strings.HasPrefix(path, "/discover/tv"):
			// Seerr v3.5.1 reads language here as an original-language filter: a tag with a region matches nothing.
			if l := r.URL.Query().Get("language"); l != "" && l != "all" && strings.Contains(l, "-") {
				_, _ = w.Write([]byte(`{"page":1,"totalPages":1,"totalResults":0,"results":[]}`))
				return
			}
			_, _ = w.Write([]byte(listJSON))
		case strings.HasPrefix(path, "/discover/"):
			_, _ = w.Write([]byte(listJSON))
		case strings.HasPrefix(path, "/movie/"), strings.HasPrefix(path, "/tv/"):
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
