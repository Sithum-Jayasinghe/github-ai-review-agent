package checks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectLanguage_Go(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if lang := detectLanguage(dir); lang != "go" {
		t.Errorf("detectLanguage = %q, want go", lang)
	}
}

func TestDetectLanguage_JavaScript(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if lang := detectLanguage(dir); lang != "javascript" {
		t.Errorf("detectLanguage = %q, want javascript", lang)
	}
}

func TestDetectLanguage_Unknown(t *testing.T) {
	dir := t.TempDir()
	if lang := detectLanguage(dir); lang != "unknown" {
		t.Errorf("detectLanguage = %q, want unknown", lang)
	}
}

func TestAllPassed_AllPass(t *testing.T) {
	results := []Result{
		{Name: "a", Status: StatusPass},
		{Name: "b", Status: StatusSkipped},
	}
	if !AllPassed(results) {
		t.Error("AllPassed should return true when no failures")
	}
}

func TestAllPassed_OneFail(t *testing.T) {
	results := []Result{
		{Name: "a", Status: StatusPass},
		{Name: "b", Status: StatusFail},
	}
	if AllPassed(results) {
		t.Error("AllPassed should return false when any check fails")
	}
}

func TestAllPassed_Empty(t *testing.T) {
	if !AllPassed(nil) {
		t.Error("AllPassed should return true for empty results")
	}
}

func TestFailedResults(t *testing.T) {
	results := []Result{
		{Name: "a", Status: StatusPass},
		{Name: "b", Status: StatusFail},
		{Name: "c", Status: StatusSkipped},
		{Name: "d", Status: StatusFail},
	}
	failed := FailedResults(results)
	if len(failed) != 2 {
		t.Errorf("FailedResults len = %d, want 2", len(failed))
	}
	for _, r := range failed {
		if r.Status != StatusFail {
			t.Errorf("expected only failed results, got %s", r.Status)
		}
	}
}

func TestFormatSummary_ContainsHeaders(t *testing.T) {
	results := []Result{
		{Name: "go-build", Status: StatusPass},
		{Name: "go-test", Status: StatusFail, Output: "FAIL: some test failed"},
	}
	summary := FormatSummary(results)
	if !strings.Contains(summary, "Automated Check Results") {
		t.Error("summary should contain section header")
	}
	if !strings.Contains(summary, "go-build") {
		t.Error("summary should contain check name go-build")
	}
	if !strings.Contains(summary, "go-test") {
		t.Error("summary should contain check name go-test")
	}
	if !strings.Contains(summary, "some test failed") {
		t.Error("summary should contain failed check output")
	}
}

func TestRunner_SkipsUnknownLanguage(t *testing.T) {
	dir := t.TempDir() // no go.mod, no package.json → unknown
	r := &Runner{WorkDir: dir}
	results := r.Run(context.Background())

	if len(results) == 0 {
		t.Fatal("expected at least one result for unknown language")
	}
	// The first result should be a skip for the language-detect check.
	found := false
	for _, res := range results {
		if res.Name == "language-detect" && res.Status == StatusSkipped {
			found = true
		}
	}
	if !found {
		t.Error("expected language-detect skipped result for unknown language")
	}
}

func TestRunner_ExtraCommands(t *testing.T) {
	dir := t.TempDir()

	r := &Runner{
		WorkDir:  dir,
		Language: "unknown", // skip standard checks
		Extra:    []string{"echo hello"},
	}
	results := r.Run(context.Background())

	var extraResult *Result
	for i, res := range results {
		if strings.HasPrefix(res.Name, "extra:") {
			extraResult = &results[i]
		}
	}
	if extraResult == nil {
		t.Fatal("expected an extra: result")
	}
	if extraResult.Status != StatusPass {
		t.Errorf("extra command should pass, got %s; output: %s", extraResult.Status, extraResult.Output)
	}
}

func TestRunner_ExtraCommandFail(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{
		WorkDir:  dir,
		Language: "unknown",
		Extra:    []string{"exit 1"},
	}
	results := r.Run(context.Background())

	var extraResult *Result
	for i, res := range results {
		if strings.HasPrefix(res.Name, "extra:") {
			extraResult = &results[i]
		}
	}
	if extraResult == nil {
		t.Fatal("expected an extra: result")
	}
	if extraResult.Status != StatusFail {
		t.Errorf("expected extra command to fail, got %s", extraResult.Status)
	}
}

func TestResult_Passed(t *testing.T) {
	cases := []struct {
		status Status
		want   bool
	}{
		{StatusPass, true},
		{StatusSkipped, true},
		{StatusFail, false},
	}
	for _, tc := range cases {
		r := Result{Status: tc.status}
		if r.Passed() != tc.want {
			t.Errorf("Result{Status:%s}.Passed() = %v, want %v", tc.status, r.Passed(), tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := truncate(long, 50)
	if len(got) <= 50 {
		// OK – truncated
	}
	if !strings.Contains(got, "truncated") {
		t.Error("truncated string should contain truncation marker")
	}

	short := "hello"
	if truncate(short, 100) != short {
		t.Error("short string should not be truncated")
	}
}
