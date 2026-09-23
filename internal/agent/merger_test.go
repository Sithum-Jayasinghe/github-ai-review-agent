package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sithum-Jayasinghe/github-ai-review-agent/config"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/github"
	"log/slog"
	"os"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// mockGitHubServer builds a minimal httptest server that responds to the
// GET /repos/:owner/:repo/pulls/:number and PUT .../merge endpoints.
func mockGitHubServer(t *testing.T, prState string, mergeable *bool, mergeConflict bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/owner/repo/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		pr := map[string]interface{}{
			"number":          1,
			"state":           prState,
			"title":           "Test PR",
			"draft":           false,
			"mergeable":       mergeable,
			"mergeable_state": "clean",
			"html_url":        "https://github.com/owner/repo/pull/1",
			"head": map[string]string{
				"ref": "feature",
				"sha": "abc123sha456",
			},
			"base": map[string]string{"ref": "main"},
			"user": map[string]string{"login": "dev"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pr)
	})

	mux.HandleFunc("/repos/owner/repo/pulls/1/merge", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if mergeConflict {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"message":"Merge conflict"}`)
			return
		}
		result := map[string]interface{}{
			"sha":     "merged_sha_abc",
			"merged":  true,
			"message": "Pull Request successfully merged",
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(result)
	})

	return httptest.NewServer(mux)
}

func newTestClient(t *testing.T, serverURL string) *github.Client {
	t.Helper()
	// We can't inject the base URL directly into the client as it's a const,
	// so we use a custom transport that redirects all requests.
	c := github.NewClientWithToken("test-token", "owner", "repo", 5*time.Second)
	// Wrap the client's transport via reflection is not possible without
	// modifying the package. Instead we test Merger behaviour via integration
	// tests against a real test server only when the server URL matches the
	// real GitHub API base. For unit tests, we test the logic paths directly.
	_ = serverURL
	return c
}

func TestMerger_DryRun(t *testing.T) {
	cfg := &config.Config{
		DryRun:           true,
		AutoMergeEnabled: true,
		MergeStrategy:    config.MergeStrategySquash,
	}

	merger := &Merger{
		Client: nil, // should never be called in dry-run
		Config: cfg,
		Logger: testLogger(),
	}

	outcome := merger.Merge(context.Background(), 1, "abc123")
	if outcome.Attempted {
		t.Error("dry-run should not attempt merge")
	}
	if !strings.Contains(outcome.Message, "dry-run") {
		t.Errorf("dry-run message = %q, want dry-run mention", outcome.Message)
	}
}

func TestMerger_AutoMergeDisabled(t *testing.T) {
	cfg := &config.Config{
		DryRun:           false,
		AutoMergeEnabled: false,
		MergeStrategy:    config.MergeStrategySquash,
	}

	merger := &Merger{
		Client: nil,
		Config: cfg,
		Logger: testLogger(),
	}

	outcome := merger.Merge(context.Background(), 1, "abc123")
	if outcome.Attempted {
		t.Error("disabled auto-merge should not attempt merge")
	}
	if !strings.Contains(outcome.Message, "disabled") {
		t.Errorf("message should mention disabled, got %q", outcome.Message)
	}
}

func TestMergeOutcome_Fields(t *testing.T) {
	o := MergeOutcome{
		Attempted: true,
		Succeeded: true,
		SHA:       "abc",
		Message:   "merged",
	}
	if !o.Attempted {
		t.Error("Attempted should be true")
	}
	if !o.Succeeded {
		t.Error("Succeeded should be true")
	}
	if o.SHA != "abc" {
		t.Errorf("SHA = %q, want abc", o.SHA)
	}
}
