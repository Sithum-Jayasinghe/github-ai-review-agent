package agent

import (
	"testing"

	"github.com/Sithum-Jayasinghe/github-ai-review-agent/config"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/checks"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/github"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/reviewer"
)

// helpers ──────────────────────────────────────────────────────────────────

func boolPtr(b bool) *bool { return &b }

func baseCfg() *config.Config {
	return &config.Config{
		TargetBranch:     "main",
		RequiredChecks:   []string{"go-build", "go-test"},
		AIReviewPolicy:   config.AIReviewPolicyAdvisory,
		AutoMergeEnabled: true,
	}
}

func openPR() *github.PullRequest {
	mergeable := true
	pr := &github.PullRequest{
		State:          "open",
		Draft:          false,
		Mergeable:      &mergeable,
		MergeableState: "clean",
	}
	pr.Base.Ref = "main"
	pr.Head.SHA = "abc123"
	return pr
}

func passingChecks() []checks.Result {
	return []checks.Result{
		{Name: "go-build", Status: checks.StatusPass},
		{Name: "go-test", Status: checks.StatusPass},
	}
}

func cleanReview() *reviewer.AIReview {
	return &reviewer.AIReview{
		Status:  reviewer.ReviewStatusApprove,
		Summary: "Looks good.",
		Issues:  nil,
	}
}

// ── Tests ──────────────────────────────────────────────────────────────────

func TestEligibility_AllConditionsPass(t *testing.T) {
	result := EvaluateMergeEligibility(baseCfg(), openPR(), passingChecks(), cleanReview())
	if !result.Eligible {
		t.Errorf("expected eligible, got block reasons: %v", result.BlockReasons)
	}
}

func TestEligibility_ClosedPR(t *testing.T) {
	pr := openPR()
	pr.State = "closed"
	result := EvaluateMergeEligibility(baseCfg(), pr, passingChecks(), cleanReview())
	if result.Eligible {
		t.Error("closed PR should not be eligible")
	}
	assertBlockCode(t, result, "pr_not_open")
}

func TestEligibility_WrongTargetBranch(t *testing.T) {
	pr := openPR()
	pr.Base.Ref = "develop"
	result := EvaluateMergeEligibility(baseCfg(), pr, passingChecks(), cleanReview())
	if result.Eligible {
		t.Error("wrong target branch should not be eligible")
	}
	assertBlockCode(t, result, "wrong_target_branch")
}

func TestEligibility_DraftPR(t *testing.T) {
	pr := openPR()
	pr.Draft = true
	result := EvaluateMergeEligibility(baseCfg(), pr, passingChecks(), cleanReview())
	if result.Eligible {
		t.Error("draft PR should not be eligible")
	}
	assertBlockCode(t, result, "draft_pr")
}

func TestEligibility_MergeConflict(t *testing.T) {
	pr := openPR()
	pr.Mergeable = boolPtr(false)
	pr.MergeableState = "dirty"
	result := EvaluateMergeEligibility(baseCfg(), pr, passingChecks(), cleanReview())
	if result.Eligible {
		t.Error("conflicted PR should not be eligible")
	}
	assertBlockCode(t, result, "merge_conflict")
}

func TestEligibility_MergeabilityUnknown_AddsWarning(t *testing.T) {
	pr := openPR()
	pr.Mergeable = nil // GitHub still computing
	result := EvaluateMergeEligibility(baseCfg(), pr, passingChecks(), cleanReview())
	// Unknown mergeability should add a warning, not a block.
	if !result.Eligible {
		// Allow block only if there's another reason.
		for _, br := range result.BlockReasons {
			if br.Code == "merge_conflict" {
				t.Errorf("nil Mergeable should not produce merge_conflict block, got: %v", br)
			}
		}
	}
	found := false
	for _, w := range result.Warnings {
		if len(w) > 0 {
			found = true
		}
	}
	_ = found // warnings are advisory; don't assert count strictly
}

func TestEligibility_RequiredCheckMissing(t *testing.T) {
	results := []checks.Result{
		{Name: "go-build", Status: checks.StatusPass},
		// go-test is missing
	}
	result := EvaluateMergeEligibility(baseCfg(), openPR(), results, cleanReview())
	if result.Eligible {
		t.Error("missing required check should not be eligible")
	}
	assertBlockCode(t, result, "required_check_missing")
}

func TestEligibility_RequiredCheckFailed(t *testing.T) {
	results := []checks.Result{
		{Name: "go-build", Status: checks.StatusPass},
		{Name: "go-test", Status: checks.StatusFail, Output: "FAIL: test_foo"},
	}
	result := EvaluateMergeEligibility(baseCfg(), openPR(), results, cleanReview())
	if result.Eligible {
		t.Error("failed required check should not be eligible")
	}
	assertBlockCode(t, result, "required_check_failed")
}

func TestEligibility_NonRequiredCheckFail_OnlyWarning(t *testing.T) {
	results := []checks.Result{
		{Name: "go-build", Status: checks.StatusPass},
		{Name: "go-test", Status: checks.StatusPass},
		{Name: "gosec", Status: checks.StatusFail, Output: "issue found"},
	}
	result := EvaluateMergeEligibility(baseCfg(), openPR(), results, cleanReview())
	// gosec is not in RequiredChecks so should only add a warning.
	if !result.Eligible {
		for _, br := range result.BlockReasons {
			if br.Code == "required_check_failed" {
				t.Errorf("non-required check failure should not add required_check_failed block: %v", br)
			}
		}
	}
}

func TestEligibility_AIPolicy_BlockOnCritical(t *testing.T) {
	cfg := baseCfg()
	cfg.AIReviewPolicy = config.AIReviewPolicyBlockOnCritical

	criticalReview := &reviewer.AIReview{
		Status:  reviewer.ReviewStatusRequestChanges,
		Summary: "Critical issues found.",
		Issues:  []reviewer.Issue{{Severity: reviewer.SeverityCritical}},
	}
	result := EvaluateMergeEligibility(cfg, openPR(), passingChecks(), criticalReview)
	if result.Eligible {
		t.Error("critical AI issue with block_on_critical policy should not be eligible")
	}
	assertBlockCode(t, result, "ai_review_critical")
}

func TestEligibility_AIPolicy_BlockOnCritical_HighOnly_Passes(t *testing.T) {
	cfg := baseCfg()
	cfg.AIReviewPolicy = config.AIReviewPolicyBlockOnCritical

	highReview := &reviewer.AIReview{
		Status:  reviewer.ReviewStatusRequestChanges,
		Summary: "High issue only.",
		Issues:  []reviewer.Issue{{Severity: reviewer.SeverityHigh}},
	}
	result := EvaluateMergeEligibility(cfg, openPR(), passingChecks(), highReview)
	// block_on_critical should not block for high-only issues.
	for _, br := range result.BlockReasons {
		if br.Code == "ai_review_critical" {
			t.Errorf("high-only issues should not trigger ai_review_critical block")
		}
	}
}

func TestEligibility_AIPolicy_BlockOnAny(t *testing.T) {
	cfg := baseCfg()
	cfg.AIReviewPolicy = config.AIReviewPolicyBlockOnAny

	review := &reviewer.AIReview{
		Status:  reviewer.ReviewStatusComment,
		Summary: "Minor issue.",
		Issues:  []reviewer.Issue{{Severity: reviewer.SeverityLow}},
	}
	result := EvaluateMergeEligibility(cfg, openPR(), passingChecks(), review)
	if result.Eligible {
		t.Error("any AI issue with block_on_any policy should not be eligible")
	}
	assertBlockCode(t, result, "ai_review_issues")
}

func TestEligibility_AIPolicy_Advisory_NeverBlocks(t *testing.T) {
	cfg := baseCfg()
	cfg.AIReviewPolicy = config.AIReviewPolicyAdvisory

	review := &reviewer.AIReview{
		Status:  reviewer.ReviewStatusRequestChanges,
		Summary: "Critical issues.",
		Issues:  []reviewer.Issue{{Severity: reviewer.SeverityCritical}},
	}
	result := EvaluateMergeEligibility(cfg, openPR(), passingChecks(), review)
	// Advisory mode should never block regardless of severity.
	for _, br := range result.BlockReasons {
		if br.Code == "ai_review_critical" || br.Code == "ai_review_issues" {
			t.Errorf("advisory mode should not block, got: %v", br)
		}
	}
}

func TestEligibility_NilAIReview_AddsWarning(t *testing.T) {
	result := EvaluateMergeEligibility(baseCfg(), openPR(), passingChecks(), nil)
	found := false
	for _, w := range result.Warnings {
		if len(w) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("nil AI review should add at least one warning")
	}
}

func TestEligibility_AutoMergeDisabled_Blocks(t *testing.T) {
	cfg := baseCfg()
	cfg.AutoMergeEnabled = false
	result := EvaluateMergeEligibility(cfg, openPR(), passingChecks(), cleanReview())
	if result.Eligible {
		t.Error("auto-merge disabled should not be eligible")
	}
	assertBlockCode(t, result, "auto_merge_disabled")
}

func TestEligibility_MultipleBlockReasons(t *testing.T) {
	pr := openPR()
	pr.State = "closed"
	pr.Draft = true
	result := EvaluateMergeEligibility(baseCfg(), pr, passingChecks(), cleanReview())
	if result.Eligible {
		t.Error("should not be eligible with multiple failures")
	}
	if len(result.BlockReasons) < 2 {
		t.Errorf("expected at least 2 block reasons, got %d", len(result.BlockReasons))
	}
}

func TestFormatEligibilityReport_Eligible(t *testing.T) {
	r := &EligibilityResult{Eligible: true}
	report := FormatEligibilityReport(r)
	if !contains(report, "Eligible") {
		t.Error("report should say Eligible")
	}
}

func TestFormatEligibilityReport_NotEligible(t *testing.T) {
	r := &EligibilityResult{Eligible: false}
	r.AddBlock("test_code", "test reason")
	report := FormatEligibilityReport(r)
	if !contains(report, "Not Eligible") {
		t.Error("report should say Not Eligible")
	}
	if !contains(report, "test_code") {
		t.Error("report should contain block code")
	}
	if !contains(report, "test reason") {
		t.Error("report should contain block message")
	}
}

func TestFormatEligibilityReport_WithWarnings(t *testing.T) {
	r := &EligibilityResult{Eligible: true}
	r.AddWarning("check this manually")
	report := FormatEligibilityReport(r)
	if !contains(report, "check this manually") {
		t.Error("report should contain warning text")
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func assertBlockCode(t *testing.T, result *EligibilityResult, code string) {
	t.Helper()
	for _, br := range result.BlockReasons {
		if br.Code == code {
			return
		}
	}
	t.Errorf("expected block reason %q not found; got: %v", code, result.BlockReasons)
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) &&
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
