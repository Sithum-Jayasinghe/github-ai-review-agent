package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Sithum-Jayasinghe/github-ai-review-agent/config"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/checks"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/github"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/reviewer"
)

// RunResult is the final summary the CLI and tests inspect.
type RunResult struct {
	PRNumber       int
	HeadSHA        string
	CheckResults   []checks.Result
	SecurityResults []checks.Result
	AIReview       *reviewer.AIReview
	Eligibility    *EligibilityResult
	Merge          *MergeOutcome
	Duration       time.Duration
	Err            error // fatal error that stopped the run
}

// Orchestrator wires together all sub-systems and drives the review workflow.
type Orchestrator struct {
	Config   *config.Config
	GitHub   *github.Client
	AI       reviewer.Provider
	Logger   *slog.Logger
	WorkDir  string // local checkout path for running checks
}

// NewOrchestrator constructs an Orchestrator from the supplied config.
// It creates the GitHub client using the appropriate auth method.
func NewOrchestrator(cfg *config.Config, workDir string, logger *slog.Logger) (*Orchestrator, error) {
	var ghClient *github.Client
	var err error

	timeout := cfg.RequestTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	if cfg.GitHubToken != "" {
		ghClient = github.NewClientWithToken(cfg.GitHubToken, cfg.GitHubOwner, cfg.GitHubRepo, timeout)
	} else {
		ghClient, err = github.NewClientWithApp(
			cfg.GitHubAppID,
			cfg.GitHubInstallationID,
			cfg.GitHubAppPrivateKey,
			cfg.GitHubOwner,
			cfg.GitHubRepo,
			timeout,
		)
		if err != nil {
			return nil, fmt.Errorf("create github app client: %w", err)
		}
	}

	aiReviewer := reviewer.NewGeminiReviewer(cfg.GeminiAPIKey, cfg.GeminiModel, timeout, logger)

	return &Orchestrator{
		Config:  cfg,
		GitHub:  ghClient,
		AI:      aiReviewer,
		Logger:  logger,
		WorkDir: workDir,
	}, nil
}

// Run executes the complete review-and-merge workflow for the configured PR.
// It always returns a RunResult so callers can inspect every step; a non-nil
// Err indicates a fatal failure that stopped the run early.
func (o *Orchestrator) Run(ctx context.Context) RunResult {
	start := time.Now()
	result := RunResult{PRNumber: o.Config.PRNumber}

	o.Logger.Info("starting PR review",
		"owner", o.Config.GitHubOwner,
		"repo", o.Config.GitHubRepo,
		"pr", o.Config.PRNumber,
		"target_branch", o.Config.TargetBranch,
		"dry_run", o.Config.DryRun,
		"auto_merge", o.Config.AutoMergeEnabled,
	)

	// ── Step 1: Fetch pull request ─────────────────────────────────────────
	o.Logger.Info("fetching pull request", "pr", o.Config.PRNumber)
	pr, err := o.GitHub.GetPullRequest(ctx, o.Config.PRNumber)
	if err != nil {
		result.Err = fmt.Errorf("fetch pull request: %w", err)
		o.Logger.Error("failed to fetch pull request", "error", result.Err)
		result.Duration = time.Since(start)
		return result
	}
	result.HeadSHA = pr.Head.SHA
	o.Logger.Info("pull request loaded",
		"title", pr.Title,
		"author", pr.User.Login,
		"head", pr.Head.Ref,
		"sha", pr.Head.SHA[:12],
		"state", pr.State,
		"draft", pr.Draft,
	)

	// Quick-exit: don't review closed PRs.
	if pr.State != "open" {
		result.Err = fmt.Errorf("pull request #%d is %s, skipping review", pr.Number, pr.State)
		o.Logger.Warn("skipping closed pull request", "state", pr.State)
		result.Duration = time.Since(start)
		return result
	}

	// ── Step 2: Fetch changed files ────────────────────────────────────────
	o.Logger.Info("fetching changed files")
	files, err := o.GitHub.ListPullRequestFiles(ctx, o.Config.PRNumber)
	if err != nil {
		result.Err = fmt.Errorf("list PR files: %w", err)
		o.Logger.Error("failed to list PR files", "error", result.Err)
		result.Duration = time.Since(start)
		return result
	}
	o.Logger.Info("changed files retrieved", "count", len(files))

	// ── Step 3: Run automated checks ───────────────────────────────────────
	o.Logger.Info("running automated checks", "workdir", o.WorkDir)
	runner := &checks.Runner{
		WorkDir: o.WorkDir,
		Extra:   o.Config.ExtraCheckCmds,
	}
	checkResults := runner.Run(ctx)
	result.CheckResults = checkResults
	logCheckResults(o.Logger, checkResults)

	// ── Step 4: Run security checks ────────────────────────────────────────
	o.Logger.Info("running security checks")
	scanner := &checks.SecurityScanner{WorkDir: o.WorkDir}
	secResults := scanner.Run(ctx)
	result.SecurityResults = secResults
	logCheckResults(o.Logger, secResults)

	// Merge security results into the main list for eligibility evaluation.
	allCheckResults := append(checkResults, secResults...)

	// ── Step 5: AI code review ─────────────────────────────────────────────
	o.Logger.Info("sending diff to AI reviewer", "model", o.Config.GeminiModel)
	prompt := reviewer.BuildPrompt(pr, files)
	aiReview, aiErr := o.AI.Review(ctx, prompt)
	if aiErr != nil {
		// AI failure is non-fatal: log the error and continue without AI results.
		o.Logger.Warn("AI review failed; continuing without AI results", "error", aiErr)
	} else {
		result.AIReview = aiReview
		o.Logger.Info("AI review completed",
			"status", aiReview.Status,
			"issues", len(aiReview.Issues),
		)
	}

	// ── Step 6: Evaluate merge eligibility ────────────────────────────────
	o.Logger.Info("evaluating merge eligibility")
	eligibility := EvaluateMergeEligibility(o.Config, pr, allCheckResults, aiReview)
	result.Eligibility = eligibility

	if eligibility.Eligible {
		o.Logger.Info("merge eligibility: ELIGIBLE")
	} else {
		o.Logger.Warn("merge eligibility: NOT ELIGIBLE",
			"block_count", len(eligibility.BlockReasons))
		for _, br := range eligibility.BlockReasons {
			o.Logger.Warn("block reason", "code", br.Code, "message", br.Message)
		}
	}
	for _, w := range eligibility.Warnings {
		o.Logger.Warn("eligibility warning", "message", w)
	}

	// ── Step 7: Publish review report ─────────────────────────────────────
	o.Logger.Info("publishing review report to GitHub")
	reporter := &Reporter{Client: o.GitHub, Logger: o.Logger}
	report := &ReviewReport{
		PRNumber:     o.Config.PRNumber,
		HeadSHA:      pr.Head.SHA,
		CheckResults: allCheckResults,
		AIReview:     aiReview,
		Eligibility:  eligibility,
		DryRun:       o.Config.DryRun,
		AgentStartedAt: start,
	}
	if err := reporter.Publish(ctx, report); err != nil {
		// Report failure is logged but doesn't stop the merge attempt.
		o.Logger.Error("failed to publish review report", "error", err)
	}

	// ── Step 8: Request merge if eligible ─────────────────────────────────
	if !eligibility.Eligible {
		o.Logger.Info("merge not requested — eligibility conditions not met")
		result.Duration = time.Since(start)
		return result
	}

	o.Logger.Info("all conditions met; requesting merge",
		"strategy", o.Config.MergeStrategy,
		"sha", pr.Head.SHA[:12],
	)
	merger := &Merger{Client: o.GitHub, Config: o.Config, Logger: o.Logger}
	outcome := merger.Merge(ctx, o.Config.PRNumber, pr.Head.SHA)
	result.Merge = &outcome

	if outcome.Err != nil {
		o.Logger.Error("merge failed",
			"attempted", outcome.Attempted,
			"message", outcome.Message,
			"error", outcome.Err,
		)
		// Post a follow-up comment explaining the merge failure.
		_ = o.postMergeFailureComment(ctx, o.Config.PRNumber, outcome)
	} else if outcome.Succeeded {
		o.Logger.Info("merge succeeded",
			"sha", outcome.SHA,
			"message", outcome.Message,
		)
	} else {
		o.Logger.Info("merge not attempted", "message", outcome.Message)
	}

	result.Duration = time.Since(start)
	o.Logger.Info("agent run complete",
		"duration", result.Duration,
		"eligible", eligibility.Eligible,
		"merged", outcome.Succeeded,
	)
	return result
}

// postMergeFailureComment notifies the PR author that the automatic merge failed.
func (o *Orchestrator) postMergeFailureComment(ctx context.Context, prNumber int, outcome MergeOutcome) error {
	body := fmt.Sprintf(
		"## ⚠️ Automatic Merge Failed\n\n"+
			"All review conditions passed, but the merge request failed:\n\n"+
			"```\n%s\n```\n\n"+
			"Please check branch protection rules, required status checks, and required approvals.\n\n"+
			"_Error: %v_",
		outcome.Message, outcome.Err,
	)
	_, err := o.GitHub.CreateIssueComment(ctx, prNumber, body)
	return err
}

// logCheckResults logs a one-liner for each check result.
func logCheckResults(logger *slog.Logger, results []checks.Result) {
	for _, r := range results {
		attrs := []any{
			"check", r.Name,
			"status", r.Status,
			"duration", r.Duration.Round(time.Millisecond),
		}
		if r.Status == checks.StatusFail {
			attrs = append(attrs, "output_preview", firstLine(r.Output))
			logger.Warn("check failed", attrs...)
		} else {
			logger.Info("check result", attrs...)
		}
	}
}

// SetupLogger creates a structured slog.Logger at the configured log level.
func SetupLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
