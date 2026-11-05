package main

import (
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kryspal/plugin-sdk/plugintest"
)

// TestConformance builds the plugin and runs the SDK conformance suite against the binary, with a fake Seerr behind it.
func TestConformance(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "plugin")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	f := &fakeSeerr{
		details: map[string]string{
			"movie:603":  movieWith(""),
			"tv:1396":    tvWith(""),
			"movie:1396": movieWith(""),
		},
		users:      `{"results":[{"id":5,"username":"plugintest"}]}`,
		postStatus: 201,
		postBody:   `{"id":7,"status":2}`,
	}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	plugintest.Run(t, bin, plugintest.Options{
		Config:        map[string]string{keyBaseURL: srv.URL, keyAPIKey: "k"},
		DiscoverQuery: "movie",
		RunWorkAction: "request",
	})
	if len(f.posts()) != 1 {
		t.Errorf("the suite ran the action %d times, want 1", len(f.posts()))
	}
}
