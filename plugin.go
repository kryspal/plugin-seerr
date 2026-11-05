package main

import (
	"context"
	_ "embed"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	pluginID = "seerr"

	keyBaseURL = "base_url"
	keyAPIKey  = "api_key"
	keyPublic  = "public_url"

	actionRequest = "request"
	checkConnect  = "test_connection"

	// providerTMDB is the only ID the request action needs: Seerr identifies titles by their TMDB number.
	providerTMDB = "tmdb"

	attributionLink = "https://github.com/seerr-team/seerr"
)

// versionFile is the single source of the plugin version: the manifest's version.
//
//go:embed VERSION
var versionFile string

func version() string { return strings.TrimSpace(versionFile) }

type plugin struct {
	pluginv1.UnimplementedPluginServer
	pluginv1.UnimplementedDiscoverSourceServer
	pluginv1.UnimplementedWorkActionProviderServer
	pluginv1.UnimplementedCheckerServer

	api    *api // replaceable in tests
	cache  *detailCache
	users  *userCache
	locale *localeCache
	titles *titleCache
	genres *genreCache
}

func newPlugin() *plugin {
	return &plugin{api: newAPI(), cache: newDetailCache(), users: newUserCache(), locale: &localeCache{now: time.Now}, titles: newTitleCache(), genres: newGenreCache()}
}

// Describe declares the two discover capabilities and the connection check. The settings are the address of the Seerr server and its API key.
func (p *plugin) Describe(_ context.Context, req *pluginv1.DescribeRequest) (*pluginv1.Manifest, error) {
	t := messages.For(req.GetLanguage())
	return &pluginv1.Manifest{
		Id: pluginID, Name: "Seerr", Description: t.T("desc"), Version: version(), ProtocolVersion: pluginv1.ProtocolVersion,
		Capabilities: []string{pluginv1.CapabilityDiscoverSource, pluginv1.CapabilityWorkAction, pluginv1.CapabilityPluginChecks},
		// Seerr usually runs on the local network.
		Permissions: []string{pluginv1.PermissionLAN},
		ConfigFields: []*pluginv1.ConfigField{
			{Key: keyBaseURL, Type: pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING, Required: true, Label: t.T("urlLabel"), Description: t.T("urlHelp"),
				Placeholder: "http://192.168.1.20:5055"},
			{Key: keyAPIKey, Type: pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_SECRET, Required: true, Label: t.T("keyLabel"), Description: t.T("keyHelp")},
			{Key: keyPublic, Type: pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING, Label: t.T("pubLabel"), Description: t.T("pubHelp")},
		},
		Checks: []*pluginv1.CheckDeclaration{{Id: checkConnect, Label: t.T("checkLabel"), Description: t.T("checkHelp")}},
		WorkActions: []*pluginv1.WorkActionDeclaration{
			{Id: actionRequest, Label: t.T("actionRequest"), RequiredProviderIds: []string{providerTMDB}},
		},
		Attribution:          &pluginv1.Attribution{Text: t.T("attribution"), Link: attributionLink},
		SuggestedConcurrency: 4,
	}, nil
}

// Configure validates the address and the key, then keeps them.
func (p *plugin) Configure(_ context.Context, req *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	t := messages.For(req.GetLanguage())
	resp := &pluginv1.ConfigureResponse{FieldErrors: map[string]string{}}
	base, errURL := normalizeBase(req.GetValues()[keyBaseURL])
	key := strings.TrimSpace(req.GetValues()[keyAPIKey])
	pub := strings.TrimSpace(req.GetValues()[keyPublic])
	if pub != "" {
		var badPub bool
		if pub, badPub = normalizeBase(pub); badPub {
			resp.FieldErrors[keyPublic] = t.T("errURL")
		}
	}
	if errURL {
		resp.FieldErrors[keyBaseURL] = t.T("errURL")
	}
	if key == "" {
		resp.FieldErrors[keyAPIKey] = t.T("errKey")
	}
	if len(resp.FieldErrors) > 0 {
		return resp, nil
	}
	p.api.set(base, key, pub)
	p.cache.clear()
	p.users.clear()
	p.locale.clear()
	p.titles.clear()
	p.genres.clear()
	return resp, nil
}

// normalizeBase returns the address without a trailing slash or a pasted /api/v1; bad is true when it is not an http or https address.
func normalizeBase(raw string) (base string, bad bool) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	s = strings.TrimSuffix(s, apiPath)
	s = strings.TrimRight(s, "/")
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", true
	}
	return s, false
}

// RunCheck tests the connection with the values on the form: Seerr answers /status, and accepts the key.
func (p *plugin) RunCheck(ctx context.Context, req *pluginv1.RunCheckRequest) (*pluginv1.RunCheckResponse, error) {
	if req.GetId() != checkConnect {
		return nil, sdk.InvalidArgument("unknown check")
	}
	t := messages.For(req.GetLanguage())
	base, bad := normalizeBase(req.GetValues()[keyBaseURL])
	key := strings.TrimSpace(req.GetValues()[keyAPIKey])
	one := func(st pluginv1.CheckStatus, msg string) (*pluginv1.RunCheckResponse, error) {
		return &pluginv1.RunCheckResponse{Results: []*pluginv1.CheckResult{{Status: st, Message: msg}}}, nil
	}
	if bad || key == "" {
		return one(pluginv1.CheckStatus_CHECK_STATUS_FAILED, t.T("checkNeedValues"))
	}
	tg := target{base: base, key: key}
	var st struct {
		Version string `json:"version"`
	}
	if err := p.api.get(ctx, tg, "/status", nil, &st); err != nil {
		return one(pluginv1.CheckStatus_CHECK_STATUS_FAILED, t.T("checkNoServer"))
	}
	var me struct {
		ID int `json:"id"`
	}
	if err := p.api.get(ctx, tg, "/auth/me", nil, &me); err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return one(pluginv1.CheckStatus_CHECK_STATUS_FAILED, t.T("checkBadKey"))
		}
		return one(pluginv1.CheckStatus_CHECK_STATUS_FAILED, t.T("checkNoServer"))
	}
	return one(pluginv1.CheckStatus_CHECK_STATUS_OK, t.Tf("checkOK", st.Version))
}

// ---- small caches ----

// detailCache keeps the answer of /movie/{id} and /tv/{id} for a few seconds, so a page of badges asks Seerr once per title.
type detailCache struct {
	mu  sync.Mutex
	m   map[string]detailEntry
	ttl time.Duration
	now func() time.Time
}

type detailEntry struct {
	at time.Time
	r  result
}

func newDetailCache() *detailCache {
	return &detailCache{m: map[string]detailEntry{}, ttl: 10 * time.Second, now: time.Now}
}

func (c *detailCache) get(key string) (result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || c.now().Sub(e.at) > c.ttl {
		return result{}, false
	}
	return e.r, true
}

func (c *detailCache) put(key string, r result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 500 {
		for k, e := range c.m {
			if c.now().Sub(e.at) > c.ttl {
				delete(c.m, k)
			}
		}
		if len(c.m) > 500 {
			c.m = map[string]detailEntry{}
		}
	}
	c.m[key] = detailEntry{at: c.now(), r: r}
}

func (c *detailCache) forget(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}

func (c *detailCache) clear() {
	c.mu.Lock()
	c.m = map[string]detailEntry{}
	c.mu.Unlock()
}

// userCache remembers which Seerr user a member is, for a few minutes. The key is the opaque member code, not the name.
type userCache struct {
	mu  sync.Mutex
	m   map[string]userEntry
	ttl time.Duration
	now func() time.Time
}

type userEntry struct {
	at time.Time
	id int
}

func newUserCache() *userCache {
	return &userCache{m: map[string]userEntry{}, ttl: 5 * time.Minute, now: time.Now}
}

func (c *userCache) get(member string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[member]
	if !ok || c.now().Sub(e.at) > c.ttl {
		return 0, false
	}
	return e.id, true
}

func (c *userCache) put(member string, id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 1000 {
		c.m = map[string]userEntry{}
	}
	c.m[member] = userEntry{at: c.now(), id: id}
}

func (c *userCache) clear() {
	c.mu.Lock()
	c.m = map[string]userEntry{}
	c.mu.Unlock()
}

// localeCache remembers Seerr's default locale for five minutes.
type localeCache struct {
	mu  sync.Mutex
	at  time.Time
	val string
	now func() time.Time
}

func (c *localeCache) get() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() || c.now().Sub(c.at) > 5*time.Minute {
		return "", false
	}
	return c.val, true
}

func (c *localeCache) put(v string) {
	c.mu.Lock()
	c.at, c.val = c.now(), v
	c.mu.Unlock()
}

func (c *localeCache) clear() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

// titleCache keeps titles looked up in a language, for a day, and runs those lookups: at most lookupSlots at a time, one per title and
// language, with their own deadline, so a call that stops waiting does not cancel them.
type titleCache struct {
	mu       sync.Mutex
	m        map[string]titleEntry
	inflight map[string]chan struct{}
	slots    chan struct{}
	budget   time.Duration // how long a call waits for lookups
	ttl      time.Duration
	now      func() time.Time
}

type titleEntry struct {
	at time.Time
	r  result
}

const (
	lookupSlots    = 8
	lookupTimeout  = 30 * time.Second
	localizeBudget = 3 * time.Second
	titleCacheMax  = 4000
)

func newTitleCache() *titleCache {
	return &titleCache{m: map[string]titleEntry{}, inflight: map[string]chan struct{}{}, slots: make(chan struct{}, lookupSlots),
		budget: localizeBudget, ttl: 24 * time.Hour, now: time.Now}
}

func titleKey(kind string, id int, lang string) string {
	return kind + ":" + strconv.Itoa(id) + ":" + lang
}

func (c *titleCache) get(kind string, id int, lang string) (result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[titleKey(kind, id, lang)]
	if !ok || c.now().Sub(e.at) > c.ttl {
		return result{}, false
	}
	return e.r, true
}

// start makes sure a lookup is cached or under way and returns a channel closed when it ends; nil when the answer is already cached.
func (c *titleCache) start(p *plugin, tg target, kind string, id int, lang string) <-chan struct{} {
	key := titleKey(kind, id, lang)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok && c.now().Sub(e.at) <= c.ttl {
		return nil
	}
	if done, ok := c.inflight[key]; ok {
		return done
	}
	done := make(chan struct{})
	c.inflight[key] = done
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
			close(done)
		}()
		c.slots <- struct{}{}
		defer func() { <-c.slots }()
		ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
		defer cancel()
		var d result
		if err := p.api.get(ctx, tg, "/"+kind+"/"+strconv.Itoa(id), url.Values{"language": {lang}}, &d); err != nil {
			return
		}
		if strings.TrimSpace(d.Title+d.Name) == "" {
			return
		}
		c.mu.Lock()
		if len(c.m) >= titleCacheMax {
			c.m = map[string]titleEntry{}
		}
		c.m[key] = titleEntry{at: c.now(), r: d}
		c.mu.Unlock()
	}()
	return done
}

func (c *titleCache) clear() {
	c.mu.Lock()
	c.m = map[string]titleEntry{}
	c.mu.Unlock()
}
