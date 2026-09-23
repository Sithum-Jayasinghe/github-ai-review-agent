package reviewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/github"
	"log/slog"
	"os"
)

// ── parseAIReview ──────────────────────────────────────────────────────────

func TestParseAIReview_ValidJSON(t *testing.T) {
	raw := `{
		"status": "request_changes",
		"summary": "Found issues.",
		"issues": [
			{
				"severity": "critical",
				"file_path": "main.go",
				"line_number": 42,
				"title": "SQL injection",
				"description": "Unsanitised input.",
				"suggestion": "Use parameterised queries."
			}
		]
	}`

	review, err := parseAIReview(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if review.Status != ReviewStatusRequestChanges {
		t.Errorf("Status = %q, want request_changes", review.Status)
	}
	if len(review.Issues) != 1 {
		t.Fatalf("Issues len = %d, want 1", len(review.Issues))
	}
	if review.Issues[0].Severity != SeverityCritical {
		t.Errorf("Severity = %q, want critical", review.Issues[0].Severity)
	}
	if review.Issues[0].LineNumber != 42 {
		t.Errorf("LineNumber = %d, want 42", review.Issues[0].LineNumber)
	}
}

func TestParseAIReview_FencedCodeBlock(t *testing.T) {
	raw := "```json\n{\"status\":\"approve\",\"summary\":\"Looks good.\",\"issues\":[]}\n```"
	review, err := parseAIReview(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if review.Status != ReviewStatusApprove {
		t.Errorf("Status = %q, want approve", review.Status)
	}
}

func TestParseAIReview_JSONWithLeadingProse(t *testing.T) {
	raw := `Here is my review:
{"status":"comment","summary":"Minor notes.","issues":[]}`
	review, err := parseAIReview(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if review.Status != ReviewStatusComment {
		t.Errorf("Status = %q, want comment", review.Status)
	}
}

func TestParseAIReview_EmptyIssues(t *testing.T) {
	raw := `{"status":"approve","summary":"All good.","issues":[]}`
	review, err := parseAIReview(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(review.Issues) != 0 {
		t.Errorf("expected empty issues, got %d", len(review.Issues))
	}
}

func TestParseAIReview_InvalidJSON(t *testing.T) {
	_, err := parseAIReview("{not valid json}")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseAIReview_NoJSON(t *testing.T) {
	_, err := parseAIReview("just some text with no braces")
	if err == nil {
		t.Error("expected error when no JSON object present")
	}
}

func TestParseAIReview_UnknownSeverityNormalised(t *testing.T) {
	raw := `{"status":"comment","summary":"x","issues":[{"severity":"extreme","file_path":"a.go","title":"t","description":"d","suggestion":"s"}]}`
	review, err := parseAIReview(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Unknown severity should be normalised to medium.
	if review.Issues[0].Severity != SeverityMedium {
		t.Errorf("Severity = %q, want medium after normalisation", review.Issues[0].Severity)
	}
}

func TestParseAIReview_UnknownStatusNormalised(t *testing.T) {
	raw := `{"status":"unknown_status","summary":"x","issues":[]}`
	review, err := parseAIReview(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if review.Status != ReviewStatusComment {
		t.Errorf("Status = %q, want comment after normalisation", review.Status)
	}
}

// ── AIReview helper methods ────────────────────────────────────────────────

func TestAIReview_HasCriticalIssues(t *testing.T) {
	r := &AIReview{
		Issues: []Issue{
			{Severity: SeverityHigh},
			{Severity: SeverityCritical},
		},
	}
	if !r.HasCriticalIssues() {
		t.Error("HasCriticalIssues should be true")
	}

	r2 := &AIReview{Issues: []Issue{{Severity: SeverityMedium}}}
	if r2.HasCriticalIssues() {
		t.Error("HasCriticalIssues should be false")
	}
}

func TestAIReview_HasHighOrAbove(t *testing.T) {
	r := &AIReview{Issues: []Issue{{Severity: SeverityHigh}}}
	if !r.HasHighOrAbove() {
		t.Error("HasHighOrAbove should be true for high")
	}

	r2 := &AIReview{Issues: []Issue{{Severity: SeverityMedium}}}
	if r2.HasHighOrAbove() {
		t.Error("HasHighOrAbove should be false for medium")
	}
}

func TestAIReview_IssuesBySeverity(t *testing.T) {
	r := &AIReview{
		Issues: []Issue{
			{Severity: SeverityCritical},
			{Severity: SeverityHigh},
			{Severity: SeverityCritical},
			{Severity: SeverityLow},
		},
	}
	criticals := r.IssuesBySeverity(SeverityCritical)
	if len(criticals) != 2 {
		t.Errorf("IssuesBySeverity(critical) len = %d, want 2", len(criticals))
	}
}

// ── BuildPrompt ────────────────────────────────────────────────────────────

func TestBuildPrompt_ContainsPRInfo(t *testing.T) {
	mergeable := true
	pr := &github.PullRequest{
		Title: "Add feature X",
		Body:  "This adds feature X",
		Head:  struct{ Ref string "json:\"ref\""; SHA string "json:\"sha\"" }{Ref: "feat/x", SHA: "abc123"},
		Base:  struct{ Ref string "json:\"ref\"" }{Ref: "main"},
		User:  struct{ Login string "json:\"login\"" }{Login: "developer"},
		Mergeable: &mergeable,
	}
	files := []github.File{
		{
			Filename:  "main.go",
			Status:    "modified",
			Additions: 10,
			Deletions: 2,
			Patch:     "@@ -1,3 +1,5 @@\n+import \"fmt\"\n func main() {}",
		},
	}

	prompt := BuildPrompt(pr, files)

	if !strings.Contains(prompt, "Add feature X") {
		t.Error("prompt should contain PR title")
	}
	if !strings.Contains(prompt, "developer") {
		t.Error("prompt should contain PR author")
	}
	if !strings.Contains(prompt, "feat/x") {
		t.Error("prompt should contain source branch")
	}
	if !strings.Contains(prompt, "main.go") {
		t.Error("prompt should contain filename")
	}
	if !strings.Contains(prompt, "JSON") {
		t.Error("prompt should instruct JSON-only response")
	}
}

func TestBuildPrompt_TruncatesLargeDiffs(t *testing.T) {
	pr := &github.PullRequest{
		Title: "Big change",
		Head:  struct{ Ref string "json:\"ref\""; SHA string "json:\"sha\"" }{Ref: "big", SHA: "abc"},
		Base:  struct{ Ref string "json:\"ref\"" }{Ref: "main"},
		User:  struct{ Login string "json:\"login\"" }{Login: "dev"},
	}

	// Create a diff larger than maxDiffBytes.
	hugePatch := strings.Repeat("+ line of code\n", 10000)
	files := []github.File{
		{Filename: "huge.go", Status: "modified", Patch: hugePatch},
	}

	prompt := BuildPrompt(pr, files)

	if !strings.Contains(prompt, "truncated") {
		t.Error("prompt should indicate truncation for large diffs")
	}
}

// ── GeminiReviewer with mock HTTP server ──────────────────────────────────

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestGeminiReviewer_Success(t *testing.T) {
	reviewJSON := `{"status":"approve","summary":"All good.","issues":[]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"candidates": []map[string]interface{}{
				{
					"content": map[string]interface{}{
						"parts": []map[string]interface{}{
							{"text": reviewJSON},
						},
					},
					"finishReason": "STOP",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// Override the API base by temporarily replacing it.
	originalBase := geminiAPIBase
	// We can't directly override the const, so we patch the reviewer's model
	// URL via a custom client pointed at the test server.
	_ = originalBase

	reviewer := &GeminiReviewer{
		APIKey:     "test-key",
		Model:      "gemini-test",
		Client:     &http.Client{Timeout: 5 * time.Second},
		Logger:     newTestLogger(),
		RetryDelay: time.Millisecond,
	}

	// Point the reviewer at the test server by replacing the URL construction.
	// Since the URL is built inside callGemini, we use a wrapper approach:
	// we test parseAIReview directly for the JSON parsing path and use the
	// mock server to verify the HTTP path end-to-end via a custom transport.
	reviewer.Client.Transport = &prefixTransport{
		prefix: srv.URL,
		orig:   http.DefaultTransport,
	}

	ctx := context.Background()
	result, err := reviewer.Review(ctx, "review this code")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != ReviewStatusApprove {
		t.Errorf("Status = %q, want approve", result.Status)
	}
}

func TestGeminiReviewer_HTTPError_NonRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	reviewer := &GeminiReviewer{
		APIKey: "bad-key",
		Model:  "gemini-test",
		Client: &http.Client{Timeout: 5 * time.Second},
		Logger: newTestLogger(),
	}
	reviewer.Client.Transport = &prefixTransport{prefix: srv.URL, orig: http.DefaultTransport}

	_, err := reviewer.Review(context.Background(), "prompt")
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestGeminiReviewer_NoCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"candidates": []interface{}{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	reviewer := &GeminiReviewer{
		APIKey: "test-key",
		Model:  "gemini-test",
		Client: &http.Client{Timeout: 5 * time.Second},
		Logger: newTestLogger(),
	}
	reviewer.Client.Transport = &prefixTransport{prefix: srv.URL, orig: http.DefaultTransport}

	_, err := reviewer.Review(context.Background(), "prompt")
	if err == nil {
		t.Fatal("expected error when no candidates returned")
	}
}

// ── FormatReviewComment ────────────────────────────────────────────────────

func TestFormatReviewComment_Approve(t *testing.T) {
	r := &AIReview{
		Status:  ReviewStatusApprove,
		Summary: "Everything looks great.",
		Issues:  nil,
	}
	comment := FormatReviewComment(r)
	if !strings.Contains(comment, "Approved") {
		t.Error("approve comment should say Approved")
	}
	if !strings.Contains(comment, "No issues found") {
		t.Error("approve comment should say no issues")
	}
}

func TestFormatReviewComment_RequestChanges(t *testing.T) {
	r := &AIReview{
		Status:  ReviewStatusRequestChanges,
		Summary: "There are critical issues.",
		Issues: []Issue{
			{
				Severity:    SeverityCritical,
				FilePath:    "auth.go",
				LineNumber:  10,
				Title:       "SQL injection",
				Description: "Unsanitised user input.",
				Suggestion:  "Use prepared statements.",
			},
		},
	}
	comment := FormatReviewComment(r)
	if !strings.Contains(comment, "Changes Requested") {
		t.Error("comment should say Changes Requested")
	}
	if !strings.Contains(comment, "SQL injection") {
		t.Error("comment should contain issue title")
	}
	if !strings.Contains(comment, "auth.go:10") {
		t.Error("comment should contain file:line location")
	}
	if !strings.Contains(comment, "prepared statements") {
		t.Error("comment should contain suggestion")
	}
}

// prefixTransport rewrites all request URLs to point to a test server.
type prefixTransport struct {
	prefix string
	orig   http.RoundTripper
}

func (t *prefixTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace scheme+host with the test server prefix.
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = "http"
	req2.URL.Host = strings.TrimPrefix(t.prefix, "http://")
	return t.orig.RoundTrip(req2)
}
