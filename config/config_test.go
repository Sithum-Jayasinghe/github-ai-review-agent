package config

import (
	"os"
	"testing"
	"time"
)

// setEnv sets multiple environment variables for the duration of the test and
// restores originals via t.Cleanup.
func setEnv(t *testing.T, pairs map[string]string) {
	t.Helper()
	for k, v := range pairs {
		original, exists := os.LookupEnv(k)
		os.Setenv(k, v)
		t.Cleanup(func() {
			if exists {
				os.Setenv(k, original)
			} else {
				os.Unsetenv(k)
			}
		})
	}
}

func minimalValidEnv() map[string]string {
	return map[string]string{
		"GITHUB_TOKEN":   "ghp_test_token",
		"GITHUB_OWNER":   "testowner",
		"GITHUB_REPO":    "testrepo",
		"GEMINI_API_KEY": "test-gemini-key",
		"PR_NUMBER":      "42",
	}
}

func TestLoad_MinimalValid(t *testing.T) {
	setEnv(t, minimalValidEnv())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.GitHubToken != "ghp_test_token" {
		t.Errorf("GitHubToken = %q, want %q", cfg.GitHubToken, "ghp_test_token")
	}
	if cfg.GitHubOwner != "testowner" {
		t.Errorf("GitHubOwner = %q, want %q", cfg.GitHubOwner, "testowner")
	}
	if cfg.GitHubRepo != "testrepo" {
		t.Errorf("GitHubRepo = %q, want %q", cfg.GitHubRepo, "testrepo")
	}
	if cfg.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", cfg.PRNumber)
	}
}

func TestLoad_Defaults(t *testing.T) {
	setEnv(t, minimalValidEnv())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want %q", cfg.TargetBranch, "main")
	}
	if cfg.GeminiModel != "gemini-1.5-pro-latest" {
		t.Errorf("GeminiModel = %q, want default", cfg.GeminiModel)
	}
	if cfg.MergeStrategy != MergeStrategySquash {
		t.Errorf("MergeStrategy = %q, want squash", cfg.MergeStrategy)
	}
	if cfg.AIReviewPolicy != AIReviewPolicyAdvisory {
		t.Errorf("AIReviewPolicy = %q, want advisory", cfg.AIReviewPolicy)
	}
	if cfg.AutoMergeEnabled {
		t.Error("AutoMergeEnabled should default to false")
	}
	if cfg.DryRun {
		t.Error("DryRun should default to false")
	}
	if cfg.RequestTimeout != 30*time.Second {
		t.Errorf("RequestTimeout = %v, want 30s", cfg.RequestTimeout)
	}
}

func TestLoad_OverrideDefaults(t *testing.T) {
	env := minimalValidEnv()
	env["TARGET_BRANCH"] = "develop"
	env["GEMINI_MODEL"] = "gemini-1.0-pro"
	env["MERGE_STRATEGY"] = "rebase"
	env["AI_REVIEW_POLICY"] = "block_on_critical"
	env["AUTO_MERGE_ENABLED"] = "true"
	env["DRY_RUN"] = "true"
	env["LOG_LEVEL"] = "debug"
	env["REQUEST_TIMEOUT_SECONDS"] = "60"
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.TargetBranch != "develop" {
		t.Errorf("TargetBranch = %q, want develop", cfg.TargetBranch)
	}
	if cfg.MergeStrategy != MergeStrategyRebase {
		t.Errorf("MergeStrategy = %q, want rebase", cfg.MergeStrategy)
	}
	if cfg.AIReviewPolicy != AIReviewPolicyBlockOnCritical {
		t.Errorf("AIReviewPolicy = %q, want block_on_critical", cfg.AIReviewPolicy)
	}
	if !cfg.AutoMergeEnabled {
		t.Error("AutoMergeEnabled should be true")
	}
	if !cfg.DryRun {
		t.Error("DryRun should be true")
	}
	if cfg.RequestTimeout != 60*time.Second {
		t.Errorf("RequestTimeout = %v, want 60s", cfg.RequestTimeout)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	cases := []struct {
		name    string
		unset   string
		wantErr string
	}{
		{"missing token", "GITHUB_TOKEN", "GITHUB_TOKEN"},
		{"missing owner", "GITHUB_OWNER", "GITHUB_OWNER"},
		{"missing repo", "GITHUB_REPO", "GITHUB_REPO"},
		{"missing gemini key", "GEMINI_API_KEY", "GEMINI_API_KEY"},
		{"missing pr number", "PR_NUMBER", "PR_NUMBER"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, minimalValidEnv())
			os.Unsetenv(tc.unset)

			_, err := Load()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestLoad_InvalidPRNumber(t *testing.T) {
	env := minimalValidEnv()
	env["PR_NUMBER"] = "not-a-number"
	setEnv(t, env)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid PR_NUMBER")
	}
}

func TestLoad_InvalidMergeStrategy(t *testing.T) {
	env := minimalValidEnv()
	env["MERGE_STRATEGY"] = "fast-forward"
	setEnv(t, env)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid MERGE_STRATEGY")
	}
}

func TestLoad_InvalidAIPolicy(t *testing.T) {
	env := minimalValidEnv()
	env["AI_REVIEW_POLICY"] = "always_merge"
	setEnv(t, env)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid AI_REVIEW_POLICY")
	}
}

func TestLoad_RequiredChecks(t *testing.T) {
	env := minimalValidEnv()
	env["REQUIRED_CHECKS"] = "go-build, go-test, go-vet"
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.RequiredChecks) != 3 {
		t.Errorf("RequiredChecks len = %d, want 3", len(cfg.RequiredChecks))
	}
	if cfg.RequiredChecks[0] != "go-build" {
		t.Errorf("RequiredChecks[0] = %q, want go-build", cfg.RequiredChecks[0])
	}
}

func TestLoad_ExtraCheckCommands(t *testing.T) {
	env := minimalValidEnv()
	env["EXTRA_CHECK_COMMANDS"] = "echo hello;echo world"
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ExtraCheckCmds) != 2 {
		t.Errorf("ExtraCheckCmds len = %d, want 2", len(cfg.ExtraCheckCmds))
	}
}

func TestLoad_GitHubAppAuth(t *testing.T) {
	env := map[string]string{
		"GITHUB_APP_ID":          "12345",
		"GITHUB_APP_PRIVATE_KEY": "-----BEGIN RSA PRIVATE KEY-----\nfake\n-----END RSA PRIVATE KEY-----",
		"GITHUB_INSTALLATION_ID": "67890",
		"GITHUB_OWNER":           "testowner",
		"GITHUB_REPO":            "testrepo",
		"GEMINI_API_KEY":         "test-gemini-key",
		"PR_NUMBER":              "1",
	}
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GitHubAppID != 12345 {
		t.Errorf("GitHubAppID = %d, want 12345", cfg.GitHubAppID)
	}
	if cfg.GitHubInstallationID != 67890 {
		t.Errorf("GitHubInstallationID = %d, want 67890", cfg.GitHubInstallationID)
	}
}
