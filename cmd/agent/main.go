// Command agent is the entry point for the GitHub AI PR Review Agent.
// It loads configuration from environment variables, wires up all subsystems,
// and runs the review-and-merge workflow for the pull request specified by
// PR_NUMBER.
//
// Usage (from the repository root being reviewed):
//
//	PR_NUMBER=42 GITHUB_TOKEN=... GEMINI_API_KEY=... \
//	  ./agent
//
// In GitHub Actions the environment is populated from workflow secrets; see
// .github/workflows/code-review.yml.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Sithum-Jayasinghe/github-ai-review-agent/config"
	"github.com/Sithum-Jayasinghe/github-ai-review-agent/internal/agent"
)

func main() {
	// Honour SIGINT / SIGTERM so the agent can clean up gracefully when
	// GitHub Actions cancels the workflow.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	// ── Configuration ───────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	// ── Logger ──────────────────────────────────────────────────────────────
	logger := agent.SetupLogger(cfg.LogLevel)
	logger.Info("agent starting",
		"version", version(),
		"pr", cfg.PRNumber,
		"repo", cfg.GitHubOwner+"/"+cfg.GitHubRepo,
		"dry_run", cfg.DryRun,
	)

	// ── Working directory ───────────────────────────────────────────────────
	// The agent runs automated checks against the local checkout. In GitHub
	// Actions this is the repository root (GITHUB_WORKSPACE). Locally it
	// defaults to the current directory.
	workDir := os.Getenv("GITHUB_WORKSPACE")
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
		workDir = wd
	}
	logger.Info("using work directory", "path", workDir)

	// ── Orchestrator ────────────────────────────────────────────────────────
	orch, err := agent.NewOrchestrator(cfg, workDir, logger)
	if err != nil {
		return fmt.Errorf("create orchestrator: %w", err)
	}

	// ── Run ─────────────────────────────────────────────────────────────────
	result := orch.Run(ctx)

	if result.Err != nil {
		return fmt.Errorf("review run: %w", result.Err)
	}

	// ── Exit code ────────────────────────────────────────────────────────────
	// Exit 2 when conditions were not met so the GitHub Actions step is marked
	// failed and the PR is clearly blocked. Exit 0 on clean approve/merge.
	if result.Eligibility != nil && !result.Eligibility.Eligible {
		// Only hard-fail when auto-merge was intended; in report-only mode
		// (DryRun or AutoMergeEnabled=false) a non-eligible result is still
		// informational.
		if cfg.AutoMergeEnabled && !cfg.DryRun {
			return fmt.Errorf("merge blocked: %d condition(s) not met",
				len(result.Eligibility.BlockReasons))
		}
	}

	if result.Merge != nil && result.Merge.Attempted && !result.Merge.Succeeded {
		return fmt.Errorf("merge attempt failed: %s", result.Merge.Message)
	}

	logger.Info("agent finished successfully", "duration", result.Duration.Round(time.Millisecond))
	return nil
}

// version returns the build version. In production this is injected via
// -ldflags "-X main.buildVersion=v1.2.3" during the release build.
var buildVersion = "dev"

func version() string { return buildVersion }
