// Package agent contains the orchestration logic: eligibility evaluation,
// review reporting, and merge coordination.
package agent

import (
	"fmt"
	"strings"

	"github.com/Sithum-Jayasinghe/github-ai-review-agent/config"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/checks"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/github"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/reviewer"
)

// BlockReason describes a single reason a merge is being blocked.
type BlockReason struct {
	Code    string // machine-readable code
	Message string // human-readable explanation
}

// EligibilityResult is the output of the merge-eligibility evaluation.
type EligibilityResult struct {
	Eligible     bool          // true only when every required condition passes
	BlockReasons []BlockReason // non-empty when Eligible is false
	Warnings     []string      // advisory notes that don't block the merge
}

// AddBlock appends a blocking reason and marks the result ineligible.
func (e *EligibilityResult) AddBlock(code, message string) {
	e.Eligible = false
	e.BlockReasons = append(e.BlockReasons, BlockReason{Code: code, Message: message})
}

// AddWarning appends a non-blocking advisory note.
func (e *EligibilityResult) AddWarning(msg string) {
	e.Warnings = append(e.Warnings, msg)
}

// EvaluateMergeEligibility checks every configured condition and returns a
// complete eligibility result. It never returns an error; every failure is
// captured as a BlockReason so the caller always gets the full picture.
func EvaluateMergeEligibility(
	cfg *config.Config,
	pr *github.PullRequest,
	checkResults []checks.Result,
	aiReview *reviewer.AIReview,
) *EligibilityResult {
	result := &EligibilityResult{Eligible: true}

	// ── 1. PR must be open ──────────────────────────────────────────────────
	if pr.State != "open" {
		result.AddBlock("pr_not_open",
			fmt.Sprintf("pull request is %q, expected \"open\"", pr.State))
	}

	// ── 2. PR must target the configured branch ─────────────────────────────
	if pr.Base.Ref != cfg.TargetBranch {
		result.AddBlock("wrong_target_branch",
			fmt.Sprintf("PR targets %q but agent is configured for %q",
				pr.Base.Ref, cfg.TargetBranch))
	}

	// ── 3. PR must not be a draft ───────────────────────────────────────────
	if pr.Draft {
		result.AddBlock("draft_pr", "pull request is in draft state")
	}

	// ── 4. No merge conflicts ────────────────────────────────────────────────
	// GitHub sets Mergeable to nil while it is still computing; treat that as
	// a soft warning rather than a hard block to avoid false positives on
	// freshly-pushed branches.
	if pr.Mergeable != nil && !*pr.Mergeable {
		result.AddBlock("merge_conflict",
			"pull request has merge conflicts that must be resolved before merging")
	}
	if pr.MergeableState == "dirty" {
		result.AddBlock("merge_conflict",
			"mergeable_state is \"dirty\" — resolve conflicts before merging")
	}
	if pr.Mergeable == nil {
		result.AddWarning("GitHub is still computing mergeability; conflict status is unknown")
	}

	// ── 5. Required automated checks must pass ──────────────────────────────
	checksByName := make(map[string]checks.Result, len(checkResults))
	for _, r := range checkResults {
		checksByName[r.Name] = r
	}

	for _, required := range cfg.RequiredChecks {
		r, ok := checksByName[required]
		if !ok {
			result.AddBlock("required_check_missing",
				fmt.Sprintf("required check %q was not executed", required))
			continue
		}
		if r.Status == checks.StatusFail {
			result.AddBlock("required_check_failed",
				fmt.Sprintf("required check %q failed: %s", required, firstLine(r.Output)))
		}
	}

	// Any check failure (even non-required) is a warning.
	for _, r := range checkResults {
		if r.Status == checks.StatusFail {
			isRequired := false
			for _, req := range cfg.RequiredChecks {
				if req == r.Name {
					isRequired = true
					break
				}
			}
			if !isRequired {
				result.AddWarning(fmt.Sprintf("non-required check %q failed", r.Name))
			}
		}
	}

	// ── 6. AI review policy ──────────────────────────────────────────────────
	if aiReview != nil {
		switch cfg.AIReviewPolicy {
		case config.AIReviewPolicyBlockOnCritical:
			if aiReview.HasCriticalIssues() {
				result.AddBlock("ai_review_critical",
					"AI reviewer found critical-severity issues that must be addressed")
			}
		case config.AIReviewPolicyBlockOnAny:
			if len(aiReview.Issues) > 0 {
				result.AddBlock("ai_review_issues",
					fmt.Sprintf("AI reviewer found %d issue(s); policy is block_on_any", len(aiReview.Issues)))
			}
		case config.AIReviewPolicyAdvisory:
			// Never blocks — advisory findings are surfaced as warnings only.
			if aiReview.HasCriticalIssues() {
				result.AddWarning("AI reviewer found critical issues (advisory mode — not blocking)")
			}
		}

		// REQUEST_CHANGES from AI always adds a warning regardless of policy.
		if aiReview.Status == reviewer.ReviewStatusRequestChanges {
			result.AddWarning("AI reviewer returned status \"request_changes\"")
		}
	} else {
		result.AddWarning("AI review was not available; skipped in eligibility check")
	}

	// ── 7. Auto-merge must be explicitly enabled ─────────────────────────────
	if !cfg.AutoMergeEnabled {
		result.AddBlock("auto_merge_disabled",
			"automatic merging is disabled; set AUTO_MERGE_ENABLED=true to enable")
	}

	return result
}

// FormatEligibilityReport returns a Markdown section describing the eligibility
// result. It is appended to the full review comment posted on the PR.
func FormatEligibilityReport(result *EligibilityResult) string {
	var sb strings.Builder

	if result.Eligible {
		sb.WriteString("## ✅ Merge Eligibility: **Eligible**\n\n")
		sb.WriteString("All required conditions are satisfied. The merge will be requested.\n\n")
	} else {
		sb.WriteString("## 🚫 Merge Eligibility: **Not Eligible**\n\n")
		sb.WriteString("The following conditions must be resolved before this PR can be merged:\n\n")
		for _, br := range result.BlockReasons {
			sb.WriteString(fmt.Sprintf("- ❌ **[%s]** %s\n", br.Code, br.Message))
		}
		sb.WriteString("\n")
	}

	if len(result.Warnings) > 0 {
		sb.WriteString("### ⚠️ Warnings\n\n")
		for _, w := range result.Warnings {
			sb.WriteString(fmt.Sprintf("- %s\n", w))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return s
}
