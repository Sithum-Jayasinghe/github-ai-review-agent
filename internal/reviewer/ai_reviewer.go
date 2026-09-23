package reviewer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	geminiAPIBase = "https://generativelanguage.googleapis.com/v1beta/models"
	maxRetries    = 3
)

// retryBaseDelay is the base duration between Gemini retries. It is a
// package-level variable (not a constant) so test code in this package can
// set it to a shorter value without modifying individual test cases.
var retryBaseDelay = 2 * time.Second

// Provider is the interface that any AI review backend must implement.
// Keeping this an interface makes it straightforward to swap Gemini for
// another provider (OpenAI, Anthropic, etc.) without changing the orchestrator.
type Provider interface {
	// Review sends a prompt and returns a structured AI review.
	Review(ctx context.Context, prompt string) (*AIReview, error)
}

// GeminiReviewer calls the Google Gemini generateContent API.
type GeminiReviewer struct {
	APIKey     string
	Model      string       // e.g. "gemini-1.5-pro-latest"
	Client     *http.Client
	Logger     *slog.Logger
	// RetryDelay overrides retryBaseDelay; useful in tests to avoid slow waits.
	RetryDelay time.Duration
}

// NewGeminiReviewer creates a GeminiReviewer with sane defaults.
func NewGeminiReviewer(apiKey, model string, timeout time.Duration, logger *slog.Logger) *GeminiReviewer {
	if model == "" {
		model = "gemini-1.5-pro-latest"
	}
	return &GeminiReviewer{
		APIKey:  apiKey,
		Model:   model,
		Client:  &http.Client{Timeout: timeout},
		Logger:  logger,
	}
}

// Review implements Provider.
func (g *GeminiReviewer) Review(ctx context.Context, prompt string) (*AIReview, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := retryBaseDelay * time.Duration(attempt)
			if g.RetryDelay > 0 {
				delay = g.RetryDelay
			}
			g.Logger.Info("retrying Gemini request", "attempt", attempt, "delay", delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		review, err := g.callGemini(ctx, prompt)
		if err == nil {
			return review, nil
		}
		lastErr = err

		// Don't retry on authentication or quota-exhausted errors.
		if isNonRetryable(err) {
			return nil, err
		}
		g.Logger.Warn("Gemini request failed, will retry", "error", err)
	}
	return nil, fmt.Errorf("gemini: all %d attempts failed: %w", maxRetries, lastErr)
}

// geminiRequest is the JSON body sent to the Gemini generateContent endpoint.
type geminiRequest struct {
	Contents []geminiContent        `json:"contents"`
	GenerationConfig geminiGenConfig `json:"generationConfig"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenConfig struct {
	Temperature     float64 `json:"temperature"`
	MaxOutputTokens int     `json:"maxOutputTokens"`
	// Ask Gemini to return JSON directly when the model supports it.
	ResponseMIMEType string `json:"responseMimeType,omitempty"`
}

// geminiResponse mirrors the Gemini API response envelope.
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
}

func (g *GeminiReviewer) callGemini(ctx context.Context, prompt string) (*AIReview, error) {
	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
		GenerationConfig: geminiGenConfig{
			Temperature:      0.2, // low temperature for deterministic, factual output
			MaxOutputTokens:  8192,
			ResponseMIMEType: "application/json",
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}

	url := fmt.Sprintf("%s/%s:generateContent?key=%s", geminiAPIBase, g.Model, g.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create gemini http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini http request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read gemini response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GeminiError{StatusCode: resp.StatusCode, Body: string(respBytes)}
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBytes, &geminiResp); err != nil {
		return nil, fmt.Errorf("decode gemini response envelope: %w", err)
	}

	// Check for content safety blocks.
	if geminiResp.PromptFeedback != nil && geminiResp.PromptFeedback.BlockReason != "" {
		return nil, fmt.Errorf("gemini blocked the prompt: %s", geminiResp.PromptFeedback.BlockReason)
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini returned no candidates")
	}

	rawText := geminiResp.Candidates[0].Content.Parts[0].Text
	if geminiResp.Candidates[0].FinishReason == "MAX_TOKENS" {
		g.Logger.Warn("Gemini response was truncated (MAX_TOKENS); attempting to parse partial JSON")
	}

	return parseAIReview(rawText)
}

// parseAIReview extracts and validates an AIReview from the Gemini text output.
// It handles the common case where the model wraps JSON in a markdown code fence.
func parseAIReview(raw string) (*AIReview, error) {
	text := strings.TrimSpace(raw)

	// Strip markdown code fences if present.
	for _, fence := range []string{"```json", "```JSON", "```"} {
		if strings.HasPrefix(text, fence) {
			text = strings.TrimPrefix(text, fence)
			text = strings.TrimSuffix(text, "```")
			text = strings.TrimSpace(text)
			break
		}
	}

	// Find the outermost JSON object in case there is leading/trailing prose.
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON object found in Gemini response (first 500 chars): %s",
			truncateStr(text, 500))
	}
	text = text[start : end+1]

	var review AIReview
	if err := json.Unmarshal([]byte(text), &review); err != nil {
		return nil, fmt.Errorf("unmarshal AI review JSON: %w (raw: %s)", err, truncateStr(text, 500))
	}

	if err := validateAIReview(&review); err != nil {
		return nil, err
	}

	return &review, nil
}

// validateAIReview checks that the parsed review has valid enum values.
func validateAIReview(r *AIReview) error {
	switch r.Status {
	case ReviewStatusApprove, ReviewStatusRequestChanges, ReviewStatusComment:
	default:
		// Normalise unexpected values rather than hard-failing.
		r.Status = ReviewStatusComment
	}

	for i, issue := range r.Issues {
		switch issue.Severity {
		case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityInfo:
		default:
			r.Issues[i].Severity = SeverityMedium
		}
	}
	return nil
}

// GeminiError is returned when the Gemini API responds with a non-200 status.
type GeminiError struct {
	StatusCode int
	Body       string
}

func (e *GeminiError) Error() string {
	return fmt.Sprintf("gemini API error %d: %s", e.StatusCode, truncateStr(e.Body, 300))
}

// isNonRetryable returns true for errors that should not trigger a retry.
func isNonRetryable(err error) bool {
	if ge, ok := err.(*GeminiError); ok {
		// 400 Bad Request, 401 Unauthorized, 403 Forbidden are not retryable.
		return ge.StatusCode == 400 || ge.StatusCode == 401 || ge.StatusCode == 403
	}
	return false
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// FormatReviewComment renders an AIReview as a Markdown PR comment body.
func FormatReviewComment(review *AIReview) string {
	var sb strings.Builder

	statusLine := map[ReviewStatus]string{
		ReviewStatusApprove:        "✅ **AI Review: Approved**",
		ReviewStatusRequestChanges: "🚨 **AI Review: Changes Requested**",
		ReviewStatusComment:        "💬 **AI Review: Comments**",
	}[review.Status]

	sb.WriteString("## " + statusLine + "\n\n")
	sb.WriteString(review.Summary + "\n\n")

	if len(review.Issues) == 0 {
		sb.WriteString("_No issues found._\n")
		return sb.String()
	}

	severityOrder := []Severity{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityInfo}
	severityLabel := map[Severity]string{
		SeverityCritical: "🔴 Critical",
		SeverityHigh:     "🟠 High",
		SeverityMedium:   "🟡 Medium",
		SeverityLow:      "🔵 Low",
		SeverityInfo:     "⚪ Info",
	}

	for _, sev := range severityOrder {
		issues := review.IssuesBySeverity(sev)
		if len(issues) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("### %s\n\n", severityLabel[sev]))
		for _, issue := range issues {
			location := issue.FilePath
			if issue.LineNumber > 0 {
				location = fmt.Sprintf("%s:%d", issue.FilePath, issue.LineNumber)
			}
			sb.WriteString(fmt.Sprintf("**%s** (`%s`)\n\n", issue.Title, location))
			sb.WriteString(issue.Description + "\n\n")
			if issue.Suggestion != "" {
				sb.WriteString("> 💡 **Suggestion:** " + issue.Suggestion + "\n\n")
			}
		}
	}

	sb.WriteString("\n---\n_This review was generated by an AI model and is advisory only. Always apply human judgment._\n")
	return sb.String()
}
