// Package checks runs automated code quality checks against a local working
// tree. Each check is isolated: it captures stdout/stderr, measures duration,
// and records a pass/fail result. The Runner collects all results so the
// orchestrator can decide whether to block the merge.
package checks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Status indicates whether a check passed or failed.
type Status string

const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusSkipped Status = "skipped"
)

// Result holds the outcome of a single check.
type Result struct {
	Name     string        // human-readable check name
	Command  string        // the command that was executed
	Status   Status        // pass | fail | skipped
	Output   string        // combined stdout + stderr
	Duration time.Duration // wall-clock time
	Error    error         // non-nil when the check could not be run at all
}

// Passed returns true when this result is not a failure.
func (r Result) Passed() bool { return r.Status == StatusPass || r.Status == StatusSkipped }

// Runner executes checks inside a repository directory.
type Runner struct {
	// WorkDir is the root of the repository to check.
	WorkDir string
	// Language hint; if empty the Runner detects the project type.
	Language string
	// Extra is an optional list of raw shell commands to append after the
	// standard checks.
	Extra []string
}

// Run executes all applicable checks and returns their results.
// It never returns an error itself; failures are captured in Result.Error /
// Result.Status so callers always get the full picture.
func (r *Runner) Run(ctx context.Context) []Result {
	lang := r.Language
	if lang == "" {
		lang = detectLanguage(r.WorkDir)
	}

	var results []Result
	switch strings.ToLower(lang) {
	case "go", "golang":
		results = append(results, r.runGoChecks(ctx)...)
	default:
		// Non-Go project: skip standard checks but still run extras.
		results = append(results, Result{
			Name:    "language-detect",
			Command: "",
			Status:  StatusSkipped,
			Output:  fmt.Sprintf("no built-in checks for language %q; running extra commands only", lang),
		})
	}

	for _, cmd := range r.Extra {
		results = append(results, r.runShell(ctx, "extra: "+cmd, cmd))
	}

	return results
}

// runGoChecks returns results for the four standard Go checks.
func (r *Runner) runGoChecks(ctx context.Context) []Result {
	return []Result{
		r.runExec(ctx, "go-fmt", "go", "fmt", "./..."),
		r.runExec(ctx, "go-vet", "go", "vet", "./..."),
		r.runExec(ctx, "go-build", "go", "build", "./..."),
		r.runExec(ctx, "go-test", "go", "test", "-v", "-count=1", "./..."),
	}
}

// runExec runs a single executable with arguments.
func (r *Runner) runExec(ctx context.Context, name, exe string, args ...string) Result {
	displayCmd := exe + " " + strings.Join(args, " ")
	start := time.Now()

	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = r.WorkDir
	// Inherit the current environment so GOPATH, GOMODCACHE etc. are available.
	cmd.Env = os.Environ()

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	dur := time.Since(start)

	status := StatusPass
	if err != nil {
		status = StatusFail
	}

	return Result{
		Name:     name,
		Command:  displayCmd,
		Status:   status,
		Output:   out.String(),
		Duration: dur,
		Error:    err,
	}
}

// runShell runs an arbitrary shell command string via the OS shell.
func (r *Runner) runShell(ctx context.Context, name, command string) Result {
	start := time.Now()

	var cmd *exec.Cmd
	if isWindows() {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = r.WorkDir
	cmd.Env = os.Environ()

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	dur := time.Since(start)

	status := StatusPass
	if err != nil {
		status = StatusFail
	}

	return Result{
		Name:     name,
		Command:  command,
		Status:   status,
		Output:   out.String(),
		Duration: dur,
		Error:    err,
	}
}

// AllPassed returns true when every result has status pass or skipped.
func AllPassed(results []Result) bool {
	for _, r := range results {
		if r.Status == StatusFail {
			return false
		}
	}
	return true
}

// FailedResults filters only the failed results.
func FailedResults(results []Result) []Result {
	var out []Result
	for _, r := range results {
		if r.Status == StatusFail {
			out = append(out, r)
		}
	}
	return out
}

// FormatSummary returns a Markdown-formatted summary of all check results.
func FormatSummary(results []Result) string {
	var sb strings.Builder
	sb.WriteString("## Automated Check Results\n\n")
	sb.WriteString("| Check | Status | Duration |\n")
	sb.WriteString("|-------|--------|----------|\n")

	for _, r := range results {
		icon := "✅"
		if r.Status == StatusFail {
			icon = "❌"
		} else if r.Status == StatusSkipped {
			icon = "⏭️"
		}
		sb.WriteString(fmt.Sprintf("| `%s` | %s %s | %s |\n",
			r.Name, icon, r.Status, r.Duration.Round(time.Millisecond)))
	}

	// Append output for failed checks.
	for _, r := range results {
		if r.Status != StatusFail {
			continue
		}
		sb.WriteString(fmt.Sprintf("\n### ❌ `%s` output\n\n```\n%s\n```\n", r.Name, truncate(r.Output, 4000)))
	}

	return sb.String()
}

// detectLanguage inspects the working directory for well-known project files.
func detectLanguage(dir string) string {
	checks := map[string]string{
		"go.mod":        "go",
		"package.json":  "javascript",
		"Cargo.toml":    "rust",
		"pom.xml":       "java",
		"build.gradle":  "java",
		"requirements.txt": "python",
		"pyproject.toml": "python",
	}
	for file, lang := range checks {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			return lang
		}
	}
	return "unknown"
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]"
}

func isWindows() bool {
	return os.PathSeparator == '\\'
}
