package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kryspal/plugin-sdk/sdk"
	"github.com/kryspal/plugin-sdk/sdk/httpclient"
)

// The Seerr API (https://github.com/seerr-team/seerr, seerr-api.yml), all under <base>/api/v1:
//
//	every request                X-Api-Key: <API key>
//	GET  /status                 {version}; no key needed
//	GET  /auth/me                the account the API key belongs to
//	GET  /search                 query, page, language → {page, totalPages, results[]}
//	GET  /discover/trending      page, language, mediaType (all, movie, tv)
//	GET  /discover/movies, /discover/tv   page, language
//	GET  /movie/{id}, /tv/{id}   details with mediaInfo {status, requests[], seasons[]}
//	GET  /user                   q → {results[]}; used to find the member's Seerr account
//	POST /request                {mediaType, mediaId, seasons}
//
// Acting for a member: the API key is an administrator's, and a request made with only the key (or with a userId in the body) is made, approved
// and counted as that administrator. The header X-API-User: <Seerr user ID> makes Seerr treat the call as that user, so the user's own
// permissions, approval rules and quotas apply. The header is not in Seerr's documentation; the tests pin the behavior (Seerr v3.5.1 and newer).

const (
	apiPath        = "/api/v1"
	requestTimeout = 20 * time.Second
	maxBody        = 8 << 20

	// imageBase serves the posters and backdrops Seerr lists by their TMDB path.
	imageBase    = "https://image.tmdb.org/t/p/"
	posterSize   = "w500"
	backdropSize = "w1280"
	cardSize     = "w780"
	logoSize     = "w185"
	cardLogoSize = "w500"
	profileSize  = "w185"
)

// api is the Seerr client. The address and key are replaced by Configure; calls in flight keep the pair they started with.
type api struct {
	mu   sync.RWMutex
	base string // without a trailing slash or /api/v1
	key  string
	pub  string // address shown to members; empty: base
	c    *httpclient.Client
}

func newAPI() *api {
	return &api{c: newHTTP(nil)}
}

func newHTTP(rt http.RoundTripper) *httpclient.Client {
	return httpclient.New(httpclient.Config{
		Name: pluginID, UserAgent: userAgent(), Timeout: requestTimeout, MaxConcurrent: 26, MaxBodyBytes: maxBody, Transport: rt,
	})
}

// userAgent is the recognizable agent string: the application name and version.
func userAgent() string { return "Kryspal Media v" + version() }

func (a *api) set(base, key, pub string) {
	a.mu.Lock()
	a.base, a.key, a.pub = base, key, pub
	a.mu.Unlock()
}

// target is the address and key one call uses.
type target struct{ base, key, publicURL string }

func (a *api) target() (target, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.base == "" || a.key == "" {
		return target{}, sdk.NeedsConfiguration("seerr: the address and the API key are not set")
	}
	return target{base: a.base, key: a.key, publicURL: a.pub}, nil
}

// get decodes a 200 answer into out. 401 and 403 mean Seerr refused the plugin's API key (not a member's credential), so they are
// FAILED_PRECONDITION; 404 is NOT_FOUND; a 429 pauses the plugin.
func (a *api) get(ctx context.Context, t target, path string, q url.Values, out any) error {
	return a.getAs(ctx, t, 0, path, q, out)
}

// getAs is get made for a Seerr user (X-API-User): Seerr then answers with what that user may see.
func (a *api) getAs(ctx context.Context, t target, userID int, path string, q url.Values, out any) error {
	resp, err := a.fetch(ctx, t, userID, path, q)
	if err != nil {
		return err
	}
	return decode(resp, out)
}

// fetch makes the GET and returns the answer whatever its status.
func (a *api) fetch(ctx context.Context, t target, userID int, path string, q url.Values) (httpclient.Response, error) {
	// Seerr wants a space as %20; url.Values.Encode writes +, which Seerr's /search answers with 400.
	u := t.base + apiPath + path
	if len(q) > 0 {
		u += "?" + strings.ReplaceAll(q.Encode(), "+", "%20")
	}
	return a.c.Do(ctx, httpclient.Request{URL: u, Header: header(t, userID)})
}

// decode maps the status of an answer and decodes a 2xx body into out.
func decode(resp httpclient.Response, out any) error {
	if err := statusError(resp); err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return sdk.Unavailable("seerr: bad response")
		}
	}
	return nil
}

// post sends a JSON body for a Seerr user (0: the key's own account) and returns the answer whatever its status, so the caller can tell a
// refusal the member should read from a failure.
func (a *api) post(ctx context.Context, t target, path string, userID int, body any) (httpclient.Response, error) {
	return a.c.Do(ctx, httpclient.Request{Method: http.MethodPost, URL: t.base + apiPath + path, Header: header(t, userID), Body: body})
}

// statusError maps a non-2xx answer; nil for 2xx.
func statusError(resp httpclient.Response) error {
	switch {
	case resp.Status >= 200 && resp.Status < 300:
		return nil
	case resp.Status == http.StatusTooManyRequests:
		return resp.RateLimit(pluginID)
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		return sdk.NeedsConfiguration("seerr: the API key was refused (" + strconv.Itoa(resp.Status) + ")")
	case resp.Status == http.StatusNotFound:
		return sdk.NotFound("seerr: not found")
	}
	return sdk.Unavailable("seerr: status " + strconv.Itoa(resp.Status))
}

// header carries the key and, when userID is not 0, the Seerr user the call is made for.
func header(t target, userID int) http.Header {
	h := http.Header{"X-Api-Key": {t.key}}
	if userID != 0 {
		h.Set("X-API-User", strconv.Itoa(userID))
	}
	return h
}

// ---- Seerr's JSON ----

// result is one entry of a search or discover list; details of /movie/{id} and /tv/{id} use the same names.
type result struct {
	ID            int    `json:"id"`
	MediaType     string `json:"mediaType"`
	Title         string `json:"title"`
	OriginalTitle string `json:"originalTitle"`
	Name          string `json:"name"`
	OriginalName  string `json:"originalName"`
	ReleaseDate   string `json:"releaseDate"`
	FirstAirDate  string `json:"firstAirDate"`
	Overview      string `json:"overview"`
	PosterPath    string `json:"posterPath"`
	BackdropPath  string `json:"backdropPath"`
	Runtime       int    `json:"runtime"`
	EpisodeRun    []int  `json:"episodeRunTime"`
	ExternalIDs   struct {
		IMDb string `json:"imdbId"`
		TVDB int    `json:"tvdbId"`
	} `json:"externalIds"`
	Releases struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Dates   []struct {
				Certification string `json:"certification"`
			} `json:"release_dates"`
		} `json:"results"`
	} `json:"releases"`
	ContentRatings struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Rating  string `json:"rating"`
		} `json:"results"`
	} `json:"contentRatings"`
	Seasons []struct {
		SeasonNumber int    `json:"seasonNumber"`
		Name         string `json:"name"`
		EpisodeCount int    `json:"episodeCount"`
		AirDate      string `json:"airDate"`
	} `json:"seasons"`
	MediaInfo *mediaInfo `json:"mediaInfo"`

	// Credits lists (/person/{id}/combined_credits).
	Character string `json:"character"`
	Job       string `json:"job"`
	GenreIDs  []int  `json:"genreIds"`

	// Details only (/movie/{id}, /tv/{id}).
	Tagline             string   `json:"tagline"`
	Genres              []named  `json:"genres"`
	Networks            []named  `json:"networks"`
	ProductionCompanies []named  `json:"productionCompanies"`
	CreatedBy           []person `json:"createdBy"`
	Credits             struct {
		Cast []person `json:"cast"`
		Crew []person `json:"crew"`
	} `json:"credits"`
	RelatedVideos []struct {
		Site string `json:"site"`
		Type string `json:"type"`
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"relatedVideos"`
}

// named is a genre, a company or a network.
type named struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// person is one entry of a credits list. createdBy gives the picture as profile_path (TMDB's own spelling), the credits as profilePath.
type person struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Character   string `json:"character"`
	Job         string `json:"job"`
	ProfilePath string `json:"profilePath"`
	ProfileRaw  string `json:"profile_path"`
}

// mediaInfo is what Seerr knows about a title's availability.
type mediaInfo struct {
	TMDBID         int `json:"tmdbId"`
	DownloadStatus []struct {
		Size     float64 `json:"size"`
		SizeLeft float64 `json:"sizeLeft"`
	} `json:"downloadStatus"`
	Status   int            `json:"status"`
	Requests []request      `json:"requests"`
	Seasons  []seasonStatus `json:"seasons"`
}

type request struct {
	ID          int        `json:"id"`
	Type        string     `json:"type"`
	UpdatedAt   string     `json:"updatedAt"`
	Media       *mediaInfo `json:"media"`
	Status      int        `json:"status"`
	Is4K        bool       `json:"is4k"`
	RequestedBy struct {
		ID int `json:"id"`
	} `json:"requestedBy"`
	Seasons []seasonStatus `json:"seasons"`
}

type seasonStatus struct {
	SeasonNumber int `json:"seasonNumber"`
	Status       int `json:"status"`
}

type page struct {
	Page       int      `json:"page"`
	TotalPages int      `json:"totalPages"`
	Results    []result `json:"results"`
}

// Seerr's media status (MediaStatus) and request status (MediaRequestStatus).
const (
	mediaUnknown     = 1
	mediaPending     = 2
	mediaProcessing  = 3
	mediaPartial     = 4
	mediaAvailable   = 5
	mediaBlocklisted = 6
	mediaDeleted     = 7

	requestPending  = 1
	requestApproved = 2
	requestDeclined = 3
	requestFailed   = 4
)

// tmdbLanguage turns a BCP 47 tag into the form Seerr's language parameter takes (en, zh-CN, zh-TW). An empty or unusable tag is English.
func tmdbLanguage(tag string) string {
	tag = strings.ReplaceAll(strings.TrimSpace(tag), "_", "-")
	switch strings.ToLower(tag) {
	case "", "und":
		return "en"
	case "zh", "zh-hans", "zh-sg", "zh-hans-cn":
		return "zh-CN"
	case "zh-hant", "zh-hk", "zh-mo", "zh-hant-tw":
		return "zh-TW"
	}
	parts := strings.Split(tag, "-")
	lang := strings.ToLower(parts[0])
	if len(lang) < 2 || len(lang) > 3 || strings.Trim(lang, "abcdefghijklmnopqrstuvwxyz") != "" {
		return "en"
	}
	for _, p := range parts[1:] {
		if len(p) == 2 {
			return lang + "-" + strings.ToUpper(p)
		}
	}
	return lang
}

func year(date string) int32 {
	if len(date) < 4 {
		return 0
	}
	n, err := strconv.Atoi(date[:4])
	if err != nil || n < 1800 {
		return 0
	}
	return int32(n)
}
