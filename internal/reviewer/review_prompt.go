// Package reviewer integrates with the Gemini API to perform AI-assisted code
// review. review_prompt.go builds the structured prompt sent to Gemini and
// defines the types used to parse the response.
package reviewer

import (
	"fmt"
	"strings"

	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/github"
)

// Severity classifies how serious an AI-identified issue is.
type Severity string

const (
	SeverityCritical Severity = "critical" // bugs, security holes, data loss
	SeverityHigh     Severity = "high"     // significant logic errors, missing error handling
	SeverityMedium   Severity = "medium"   // code quality, maintainability
	SeverityLow      Severity = "low"      // style, minor improvements
	SeverityInfo     Severity = "info"     // informational, no action required
)

// ReviewStatus is the overall verdict returned by the AI.
type ReviewStatus string

const (
	ReviewStatusApprove         ReviewStatus = "approve"          // no blocking issues
	ReviewStatusRequestChanges  ReviewStatus = "request_changes"  // blocking issues found
	ReviewStatusComment         ReviewStatus = "comment"          // advisory notes only
)

// Issue is a single finding from the AI reviewer.
type Issue struct {
	Severity    Severity `json:"severity"`
	FilePath    string   `json:"file_path"`
	LineNumber  int      `json:"line_number,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Suggestion  string   `json:"suggestion"`
}

// AIReview is the structured response we expect from Gemini.
type AIReview struct {
	Status  ReviewStatus `json:"status"`
	Summary string       `json:"summary"`
	Issues  []Issue      `json:"issues"`
}

// HasCriticalIssues returns true when any issue is critical severity.
func (r *AIReview) HasCriticalIssues() bool {
	for _, i := range r.Issues {
		if i.Severity == SeverityCritical {
			return true
		}
	}
	return false
}

// HasHighOrAbove returns true when any issue is high or critical severity.
func (r *AIReview) HasHighOrAbove() bool {
	for _, i := range r.Issues {
		if i.Severity == SeverityCritical || i.Severity == SeverityHigh {
			return true
		}
	}
	return false
}

// IssuesBySeverity returns issues filtered to the given severity level.
func (r *AIReview) IssuesBySeverity(s Severity) []Issue {
	var out []Issue
	for _, i := range r.Issues {
		if i.Severity == s {
			out = append(out, i)
		}
	}
	return out
}

// maxDiffBytes is the maximum number of bytes of diff we send to the AI.
// Gemini 1.5 Pro has a ~1 M token context window; we stay well under it.
const maxDiffBytes = 120_000

// BuildPrompt constructs the full prompt sent to Gemini.
// pr is the pull request metadata; files are the changed files with diffs.
func BuildPrompt(pr *github.PullRequest, files []github.File) string {
	var sb strings.Builder

	sb.WriteString(`You are a senior software engineer performing a thorough code review.
Analyse the following pull request diff and respond with a JSON object that EXACTLY matches this schema (no extra text before or after the JSON):

{
  "status": "<approve|request_changes|comment>",
  "summary": "<one-paragraph overall assessment>",
  "issues": [
    {
      "severity": "<critical|high|medium|low|info>",
      "file_path": "<path/to/file.go>",
      "line_number": <integer or 0 if unknown>,
      "title": "<short title>",
      "description": "<what is wrong and why it matters>",
      "suggestion": "<concrete improvement or fix>"
    }
  ]
}

Severity guidelines:
- critical : security vulnerabilities, crashes, data corruption, or data loss
- high     : incorrect logic, missing error handling, race conditions, panics
- medium   : code quality, readability, missing tests for important paths
- low      : style, naming, minor improvements
- info     : observations, suggestions, praise

Return status "approve" when you find no blocking issues.
Return status "request_changes" when you find critical or high severity issues.
Return status "comment" when you find only medium/low/info issues.

If there are no issues, return an empty issues array.
Do NOT wrap the JSON in a code fence.

`)

	sb.WriteString("## Pull Request\n\n")
	sb.WriteString(fmt.Sprintf("**Title:** %s\n", pr.Title))
	sb.WriteString(fmt.Sprintf("**Author:** %s\n", pr.User.Login))
	sb.WriteString(fmt.Sprintf("**Source branch:** `%s` → **Target branch:** `%s`\n\n", pr.Head.Ref, pr.Base.Ref))
	if pr.Body != "" {
		sb.WriteString("**Description:**\n")
		sb.WriteString(pr.Body)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Changed Files\n\n")

	totalBytes := 0
	truncated := false

	for _, f := range files {
		if totalBytes >= maxDiffBytes {
			truncated = true
			break
		}

		header := fmt.Sprintf("### `%s` (%s, +%d/-%d)\n\n",
			f.Filename, f.Status, f.Additions, f.Deletions)
		sb.WriteString(header)

		if f.Patch == "" {
			sb.WriteString("_Binary file or diff not available._\n\n")
			continue
		}

		// Trim the patch if adding it would exceed the byte budget.
		patch := f.Patch
		remaining := maxDiffBytes - totalBytes - len(header)
		if len(patch) > remaining {
			patch = patch[:remaining]
			truncated = true
		}

		sb.WriteString("```diff\n")
		sb.WriteString(patch)
		sb.WriteString("\n```\n\n")
		totalBytes += len(header) + len(patch)
	}

	if truncated {
		sb.WriteString("> ⚠️ Diff truncated to stay within context limits. Review remaining files manually.\n\n")
	}

	return sb.String()
}
