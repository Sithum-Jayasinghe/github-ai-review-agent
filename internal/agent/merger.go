package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Sithum-Jayasinghe/github-ai-review-agent/config"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/github"
)

// MergeOutcome describes what happened when we attempted to merge.
type MergeOutcome struct {
	Attempted bool
	Succeeded bool
	SHA       string // merge commit SHA when Succeeded is true
	Message   string // human-readable description
	Err       error  // non-nil when the merge attempt failed
}

// Merger handles the final merge step.
type Merger struct {
	Client *github.Client
	Config *config.Config
	Logger *slog.Logger
}

// Merge attempts to merge the PR after verifying all safety conditions.
// It returns a MergeOutcome regardless of success so the orchestrator can
// log and report the result.
//
// Security considerations:
//   - We re-fetch the PR immediately before merging to catch last-second changes.
//   - We supply the expected HeadSHA to the GitHub API so a concurrent push
//     cannot slip in between our check and the merge request.
//   - We never call merge when DryRun is true.
//   - Auto-merge must be explicitly enabled via configuration.
func (m *Merger) Merge(ctx context.Context, prNumber int, expectedHeadSHA string) MergeOutcome {
	if m.Config.DryRun {
		return MergeOutcome{
			Attempted: false,
			Message:   "dry-run mode: merge skipped",
		}
	}

	if !m.Config.AutoMergeEnabled {
		return MergeOutcome{
			Attempted: false,
			Message:   "auto-merge is disabled; set AUTO_MERGE_ENABLED=true to enable",
		}
	}

	// Re-fetch the PR to get the freshest state.
	pr, err := m.Client.GetPullRequest(ctx, prNumber)
	if err != nil {
		return MergeOutcome{
			Attempted: true,
			Message:   "could not re-fetch PR before merge",
			Err:       fmt.Errorf("re-fetch pr: %w", err),
		}
	}

	// Verify the head SHA has not changed since we reviewed it.
	if pr.Head.SHA != expectedHeadSHA {
		return MergeOutcome{
			Attempted: false,
			Message: fmt.Sprintf(
				"head SHA changed from %s to %s after review; aborting merge to avoid merging unreviewed code",
				expectedHeadSHA[:12], pr.Head.SHA[:12]),
		}
	}

	// Final state checks (belt-and-suspenders on top of eligibility).
	if pr.State != "open" {
		return MergeOutcome{
			Attempted: false,
			Message:   fmt.Sprintf("PR is %q, expected open; merge aborted", pr.State),
		}
	}
	if pr.Draft {
		return MergeOutcome{
			Attempted: false,
			Message:   "PR became a draft; merge aborted",
		}
	}
	if pr.Mergeable != nil && !*pr.Mergeable {
		return MergeOutcome{
			Attempted: false,
			Message:   "PR has merge conflicts; resolve them before merging",
		}
	}

	// Wait briefly if GitHub is still computing mergeability.
	if pr.Mergeable == nil || pr.MergeableState == "unknown" {
		m.Logger.Info("waiting for GitHub to compute mergeability", "pr", prNumber)
		time.Sleep(5 * time.Second)
		pr, err = m.Client.GetPullRequest(ctx, prNumber)
		if err != nil {
			return MergeOutcome{
				Attempted: true,
				Err:       fmt.Errorf("re-fetch pr after mergeability wait: %w", err),
			}
		}
		if pr.Mergeable != nil && !*pr.Mergeable {
			return MergeOutcome{
				Attempted: false,
				Message:   "PR has merge conflicts after re-check; merge aborted",
			}
		}
	}

	commitTitle := fmt.Sprintf("%s (#%d)", pr.Title, prNumber)
	commitMsg := fmt.Sprintf(
		"Automatically merged by AI Review Agent\n\nPR: %s\nAuthor: %s\nReviewed at: %s",
		pr.HTMLURL, pr.User.Login, time.Now().UTC().Format(time.RFC3339))

	req := github.MergeRequest{
		CommitTitle:   commitTitle,
		CommitMessage: commitMsg,
		SHA:           expectedHeadSHA,
		MergeMethod:   string(m.Config.MergeStrategy),
	}

	m.Logger.Info("requesting merge",
		"pr", prNumber,
		"sha", expectedHeadSHA[:12],
		"method", m.Config.MergeStrategy)

	result, err := m.Client.MergePullRequest(ctx, prNumber, req)
	if err != nil {
		// Distinguish conflict vs protection rules vs other errors.
		if errors.Is(err, github.ErrMergeConflict) {
			return MergeOutcome{
				Attempted: true,
				Message:   "merge failed: pull request has conflicts",
				Err:       err,
			}
		}
		if errors.Is(err, github.ErrMergeNotAllowed) {
			return MergeOutcome{
				Attempted: true,
				Message:   "merge blocked by branch protection rules; ensure all required status checks and approvals are satisfied",
				Err:       err,
			}
		}
		return MergeOutcome{
			Attempted: true,
			Message:   "merge request failed",
			Err:       err,
		}
	}

	return MergeOutcome{
		Attempted: true,
		Succeeded: result.Merged,
		SHA:       result.SHA,
		Message:   result.Message,
	}
}
