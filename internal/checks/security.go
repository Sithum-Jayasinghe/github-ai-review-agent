package checks

import (
	"context"
	"os/exec"
	"strings"
)

// SecurityScanner runs optional static-security tools when they are available
// in PATH. All results are advisory – a missing tool is reported as skipped,
// not as a failure.
type SecurityScanner struct {
	WorkDir string
}

// Run attempts each supported security scanner and returns the results.
func (s *SecurityScanner) Run(ctx context.Context) []Result {
	r := &Runner{WorkDir: s.WorkDir}
	var results []Result

	// gosec – Go security checker
	if commandExists("gosec") {
		results = append(results, r.runExec(ctx, "gosec", "gosec", "-fmt=text", "-quiet", "./..."))
	} else {
		results = append(results, Result{
			Name:   "gosec",
			Status: StatusSkipped,
			Output: "gosec not found in PATH; install with: go install github.com/securecodewarrior/gosec/v2/cmd/gosec@latest",
		})
	}

	// staticcheck – comprehensive static analysis
	if commandExists("staticcheck") {
		results = append(results, r.runExec(ctx, "staticcheck", "staticcheck", "./..."))
	} else {
		results = append(results, Result{
			Name:   "staticcheck",
			Status: StatusSkipped,
			Output: "staticcheck not found in PATH; install with: go install honnef.co/go/tools/cmd/staticcheck@latest",
		})
	}

	// govulncheck – vulnerability database check
	if commandExists("govulncheck") {
		results = append(results, r.runExec(ctx, "govulncheck", "govulncheck", "./..."))
	} else {
		results = append(results, Result{
			Name:   "govulncheck",
			Status: StatusSkipped,
			Output: "govulncheck not found in PATH; install with: go install golang.org/x/vuln/cmd/govulncheck@latest",
		})
	}

	return results
}

// commandExists returns true when the named executable is found in PATH.
func commandExists(name string) bool {
	// Quick check without executing the binary.
	_, err := exec.LookPath(name)
	return err == nil
}

// HasSecurityFailures returns true when any security check failed (not skipped).
func HasSecurityFailures(results []Result) bool {
	for _, r := range results {
		if r.Status == StatusFail && strings.HasPrefix(r.Name, "gosec") ||
			r.Name == "govulncheck" && r.Status == StatusFail {
			return true
		}
	}
	return false
}
