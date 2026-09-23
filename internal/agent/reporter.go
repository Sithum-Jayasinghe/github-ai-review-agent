package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/checks"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/github"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/reviewer"
)

// Reporter posts review results back to the GitHub pull request.
type Reporter struct {
	Client *github.Client
	Logger *slog.Logger
}

// ReviewReport is the combined payload the Reporter posts to GitHub.
type ReviewReport struct {
	PRNumber       int
	HeadSHA        string
	CheckResults   []checks.Result
	AIReview       *reviewer.AIReview // may be nil if AI was unavailable
	Eligibility    *EligibilityResult
	DryRun         bool
	AgentStartedAt time.Time
}

// Publish posts the consolidated review comment and, where appropriate, a
// formal GitHub PR review (APPROVE / REQUEST_CHANGES / COMMENT).
func (r *Reporter) Publish(ctx context.Context, report *ReviewReport) error {
	body := r.buildCommentBody(report)

	if report.DryRun {
		r.Logger.Info("dry-run mode: skipping GitHub comment post",
			"pr", report.PRNumber,
			"comment_length", len(body))
		return nil
	}

	// Post the consolidated comment.
	comment, err := r.Client.CreateIssueComment(ctx, report.PRNumber, body)
	if err != nil {
		return fmt.Errorf("post review comment: %w", err)
	}
	r.Logger.Info("posted review comment", "pr", report.PRNumber, "comment_id", comment.ID)

	// Submit a formal GitHub review so the PR review UI is updated.
	event := r.reviewEvent(report)
	reviewReq := github.ReviewRequest{
		CommitID: report.HeadSHA,
		Body:     r.reviewSummaryLine(report),
		Event:    event,
	}

	rev, err := r.Client.CreateReview(ctx, report.PRNumber, reviewReq)
	if err != nil {
		// A review failure is logged but not fatal — the comment was already posted.
		r.Logger.Warn("could not submit formal PR review", "error", err)
	} else {
		r.Logger.Info("submitted PR review", "pr", report.PRNumber, "review_id", rev.ID, "event", event)
	}

	// Apply labels to make the PR status visible at a glance.
	if err := r.updateLabels(ctx, report); err != nil {
		r.Logger.Warn("failed to update PR labels", "error", err)
	}

	return nil
}

// buildCommentBody assembles the full Markdown comment from all review sections.
func (r *Reporter) buildCommentBody(report *ReviewReport) string {
	var sb strings.Builder

	// Header
	sb.WriteString("# 🤖 AI PR Review Agent Report\n\n")
	sb.WriteString(fmt.Sprintf("_Reviewed commit `%s` at %s_\n\n",
		report.HeadSHA[:min(len(report.HeadSHA), 12)],
		report.AgentStartedAt.UTC().Format("2006-01-02 15:04:05 UTC")))
	sb.WriteString("---\n\n")

	// Automated checks section.
	sb.WriteString(checks.FormatSummary(report.CheckResults))
	sb.WriteString("\n")

	// AI review section.
	if report.AIReview != nil {
		sb.WriteString(reviewer.FormatReviewComment(report.AIReview))
		sb.WriteString("\n")
	} else {
		sb.WriteString("## ⚠️ AI Review Unavailable\n\nThe AI review could not be completed. Check the agent logs for details.\n\n")
	}

	// Eligibility section.
	sb.WriteString(FormatEligibilityReport(report.Eligibility))

	// Footer
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("_Agent run duration: %s_\n",
		time.Since(report.AgentStartedAt).Round(time.Millisecond)))

	return sb.String()
}

// reviewEvent maps the eligibility result to a GitHub review event string.
func (r *Reporter) reviewEvent(report *ReviewReport) string {
	if !report.Eligibility.Eligible {
		return "REQUEST_CHANGES"
	}
	if report.AIReview != nil && report.AIReview.Status == reviewer.ReviewStatusRequestChanges {
		return "REQUEST_CHANGES"
	}
	return "COMMENT"
}

// reviewSummaryLine is the one-liner body on the formal GitHub review.
func (r *Reporter) reviewSummaryLine(report *ReviewReport) string {
	if !report.Eligibility.Eligible {
		codes := make([]string, 0, len(report.Eligibility.BlockReasons))
		for _, br := range report.Eligibility.BlockReasons {
			codes = append(codes, br.Code)
		}
		return fmt.Sprintf("Merge blocked: %s", strings.Join(codes, ", "))
	}
	return "All checks passed. Ready to merge."
}

// updateLabels adds/removes the agent-managed labels on the PR.
func (r *Reporter) updateLabels(ctx context.Context, report *ReviewReport) error {
	const (
		labelPassed = "ai-review: passed"
		labelFailed = "ai-review: changes-requested"
	)

	if report.Eligibility.Eligible {
		_ = r.Client.RemoveLabel(ctx, report.PRNumber, labelFailed)
		return r.Client.AddLabel(ctx, report.PRNumber, labelPassed)
	}
	_ = r.Client.RemoveLabel(ctx, report.PRNumber, labelPassed)
	return r.Client.AddLabel(ctx, report.PRNumber, labelFailed)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
