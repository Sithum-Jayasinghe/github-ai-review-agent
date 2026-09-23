// Package config loads and validates all runtime configuration from environment
// variables. A single Config struct is passed through the application so that
// nothing reads os.Getenv after start-up.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// MergeStrategy controls which GitHub merge method is requested.
type MergeStrategy string

const (
	MergeStrategyMerge  MergeStrategy = "merge"
	MergeStrategySquash MergeStrategy = "squash"
	MergeStrategyRebase MergeStrategy = "rebase"
)

// AIReviewPolicy controls when AI findings block a merge.
type AIReviewPolicy string

const (
	// AIReviewPolicyAdvisory – AI results are posted as comments but never block.
	AIReviewPolicyAdvisory AIReviewPolicy = "advisory"
	// AIReviewPolicyBlockOnCritical – merge is blocked when the AI finds critical issues.
	AIReviewPolicyBlockOnCritical AIReviewPolicy = "block_on_critical"
	// AIReviewPolicyBlockOnAny – merge is blocked when the AI finds any issue.
	AIReviewPolicyBlockOnAny AIReviewPolicy = "block_on_any"
)

// Config holds all application configuration.
type Config struct {
	// GitHub credentials
	GitHubToken string // personal access token or GitHub App installation token

	// GitHub App credentials (optional – used when GitHubToken is empty)
	GitHubAppID          int64
	GitHubAppPrivateKey  string // PEM-encoded private key, newlines as \n
	GitHubInstallationID int64

	// Repository context
	GitHubOwner  string
	GitHubRepo   string
	TargetBranch string // branch PRs must target (default: main)

	// Pull-request context (injected by GitHub Actions)
	PRNumber int

	// Gemini
	GeminiAPIKey string
	GeminiModel  string // e.g. gemini-1.5-pro-latest

	// Checks
	RequiredChecks []string // names of checks that must pass before merge
	ExtraCheckCmds []string // additional shell commands to run as checks

	// AI review policy
	AIReviewPolicy AIReviewPolicy

	// Merge
	AutoMergeEnabled bool
	MergeStrategy    MergeStrategy

	// Behaviour
	LogLevel        string
	DryRun          bool // post comments but never request a merge
	RequestTimeout  time.Duration
}

// Load reads configuration from environment variables and validates it.
// It returns an error that lists every missing or invalid value so the caller
// can surface all problems at once.
func Load() (*Config, error) {
	cfg := &Config{
		TargetBranch:   getEnvDefault("TARGET_BRANCH", "main"),
		GeminiModel:    getEnvDefault("GEMINI_MODEL", "gemini-1.5-pro-latest"),
		MergeStrategy:  MergeStrategy(getEnvDefault("MERGE_STRATEGY", string(MergeStrategySquash))),
		AIReviewPolicy: AIReviewPolicy(getEnvDefault("AI_REVIEW_POLICY", string(AIReviewPolicyAdvisory))),
		LogLevel:       getEnvDefault("LOG_LEVEL", "info"),
		RequestTimeout: parseDuration(os.Getenv("REQUEST_TIMEOUT_SECONDS"), 30*time.Second),
	}

	cfg.GitHubToken = os.Getenv("GITHUB_TOKEN")
	cfg.GitHubAppPrivateKey = strings.ReplaceAll(os.Getenv("GITHUB_APP_PRIVATE_KEY"), `\n`, "\n")

	if v := os.Getenv("GITHUB_APP_ID"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("GITHUB_APP_ID is not a valid integer: %w", err)
		}
		cfg.GitHubAppID = id
	}
	if v := os.Getenv("GITHUB_INSTALLATION_ID"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("GITHUB_INSTALLATION_ID is not a valid integer: %w", err)
		}
		cfg.GitHubInstallationID = id
	}

	cfg.GitHubOwner = os.Getenv("GITHUB_OWNER")
	cfg.GitHubRepo = os.Getenv("GITHUB_REPO")
	cfg.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")

	if v := os.Getenv("PR_NUMBER"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("PR_NUMBER is not a valid integer: %w", err)
		}
		cfg.PRNumber = n
	}

	cfg.AutoMergeEnabled = parseBool(os.Getenv("AUTO_MERGE_ENABLED"), false)
	cfg.DryRun = parseBool(os.Getenv("DRY_RUN"), false)

	if v := os.Getenv("REQUIRED_CHECKS"); v != "" {
		cfg.RequiredChecks = splitTrimmed(v, ",")
	} else {
		cfg.RequiredChecks = []string{"build", "vet", "test"}
	}

	if v := os.Getenv("EXTRA_CHECK_COMMANDS"); v != "" {
		cfg.ExtraCheckCmds = splitTrimmed(v, ";")
	}

	return cfg, cfg.validate()
}

// validate returns a combined error listing every invalid field.
func (c *Config) validate() error {
	var errs []string

	// Require at least one authentication method.
	hasToken := c.GitHubToken != ""
	hasApp := c.GitHubAppID != 0 && c.GitHubAppPrivateKey != "" && c.GitHubInstallationID != 0
	if !hasToken && !hasApp {
		errs = append(errs, "provide GITHUB_TOKEN, or all three of GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY, GITHUB_INSTALLATION_ID")
	}

	if c.GitHubOwner == "" {
		errs = append(errs, "GITHUB_OWNER is required")
	}
	if c.GitHubRepo == "" {
		errs = append(errs, "GITHUB_REPO is required")
	}
	if c.GeminiAPIKey == "" {
		errs = append(errs, "GEMINI_API_KEY is required")
	}
	if c.PRNumber <= 0 {
		errs = append(errs, "PR_NUMBER must be a positive integer")
	}

	switch c.MergeStrategy {
	case MergeStrategyMerge, MergeStrategySquash, MergeStrategyRebase:
	default:
		errs = append(errs, fmt.Sprintf("MERGE_STRATEGY must be merge, squash, or rebase; got %q", c.MergeStrategy))
	}

	switch c.AIReviewPolicy {
	case AIReviewPolicyAdvisory, AIReviewPolicyBlockOnCritical, AIReviewPolicyBlockOnAny:
	default:
		errs = append(errs, fmt.Sprintf("AI_REVIEW_POLICY must be advisory, block_on_critical, or block_on_any; got %q", c.AIReviewPolicy))
	}

	if len(errs) > 0 {
		return errors.New("configuration errors:\n  - " + strings.Join(errs, "\n  - "))
	}
	return nil
}

// Helpers ---------------------------------------------------------------

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseBool(s string, def bool) bool {
	if s == "" {
		return def
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return v
}

func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	seconds, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return time.Duration(seconds) * time.Second
}

func splitTrimmed(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
