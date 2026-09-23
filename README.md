# GitHub AI PR Review Agent

An automated pull-request review and merge agent written in Go. When a developer pushes to a feature branch and opens a PR targeting `main`, the agent:

1. Retrieves the PR metadata and changed files from GitHub.
2. Runs automated checks (`go fmt`, `go vet`, `go build`, `go test`, plus any custom commands).
3. Sends the diff to the **Google Gemini** API for AI-assisted code review.
4. Posts a consolidated Markdown report as a PR comment and submits a formal GitHub review.
5. Requests a merge automatically when every configured requirement is satisfied.

---

## Table of Contents

1. [Architecture](#architecture)
2. [Prerequisites](#prerequisites)
3. [Go environment setup](#go-environment-setup)
4. [GitHub App creation and permissions](#github-app-creation-and-permissions)
5. [Gemini API configuration](#gemini-api-configuration)
6. [Environment variable reference](#environment-variable-reference)
7. [GitHub Actions setup](#github-actions-setup)
8. [Branch protection configuration](#branch-protection-configuration)
9. [Running the agent locally](#running-the-agent-locally)
10. [Testing with a sample pull request](#testing-with-a-sample-pull-request)
11. [Enabling automatic merging safely](#enabling-automatic-merging-safely)
12. [Troubleshooting](#troubleshooting)

---

## Architecture

```
github-ai-review-agent/
├── cmd/agent/main.go              # CLI entry point
├── internal/
│   ├── github/
│   │   ├── client.go              # Authenticated HTTP client, retry, rate-limit handling
│   │   ├── pull_request.go        # PR fetch, file list, reviews, check-runs, labels
│   │   └── merge.go               # Merge request, auto-merge (GraphQL), sentinel errors
│   ├── checks/
│   │   ├── runner.go              # Language detection, go fmt/vet/build/test, extra cmds
│   │   └── security.go            # gosec, staticcheck, govulncheck (skipped if absent)
│   ├── reviewer/
│   │   ├── review_prompt.go       # Prompt builder, AIReview/Issue/Severity types
│   │   └── ai_reviewer.go         # GeminiReviewer, Provider interface, JSON parsing
│   └── agent/
│       ├── orchestrator.go        # 8-step workflow, NewOrchestrator, SetupLogger
│       ├── eligibility.go         # EvaluateMergeEligibility (7 conditions)
│       ├── reporter.go            # Reporter.Publish – comment + formal review + labels
│       └── merger.go              # Merger.Merge – SHA verification, conflict handling
├── config/config.go               # Load() from env vars, validation
├── .github/workflows/
│   └── code-review.yml            # go-checks job + ai-review job
├── .env.example
└── .gitignore
```

**Key design decisions:**

- **`reviewer.Provider` interface** — swapping Gemini for another AI provider requires only a new struct that implements `Review(ctx, prompt) (*AIReview, error)`.
- **SHA verification before merge** — the agent re-fetches the PR immediately before calling the merge API and supplies the expected head SHA, preventing race conditions with concurrent pushes.
- **AI results are advisory by default** — the `AI_REVIEW_POLICY=advisory` default means the AI never blocks a merge on its own. Human approvals and GitHub branch-protection rules remain the authoritative gate.
- **No privileged code execution** — automated checks run in the GitHub Actions runner environment against the checked-out source, not inside a container with live credentials.

---

## Prerequisites

| Requirement | Minimum version | Notes |
|---|---|---|
| Go | 1.21 | Uses `log/slog` (stdlib) |
| GitHub account | — | With repository admin access |
| Google AI Studio account | — | For Gemini API key |
| GitHub Actions | — | Enabled on the repository |

---

## Go environment setup

```bash
# Verify Go is installed
go version   # should print go1.21 or later

# Clone the repository
git clone https://github.com/yourorg/github-ai-review-agent.git
cd github-ai-review-agent

# Download dependencies
go mod download

# Build the agent binary
go build -o review-agent ./cmd/agent

# Run tests
go test ./...
```

---

## GitHub App creation and permissions

Using a GitHub App is recommended for production because it provides scoped permissions, short-lived tokens, and a clear audit trail.

### Step 1 – Create the App

1. Go to **GitHub → Settings → Developer settings → GitHub Apps → New GitHub App**.
2. Fill in:
   - **GitHub App name**: `AI PR Review Agent` (or any unique name)
   - **Homepage URL**: your repository URL
   - **Webhook**: uncheck *Active* (the agent is triggered by Actions, not webhooks)
3. Set **Repository permissions**:
   | Permission | Access |
   |---|---|
   | Contents | Read & write (needed to merge) |
   | Pull requests | Read & write |
   | Checks | Read |
   | Issues | Read & write (PR comments use the Issues API) |
4. Click **Create GitHub App**.

### Step 2 – Generate a private key

On the App settings page, scroll to **Private keys** and click **Generate a private key**. Download the `.pem` file and store it securely — you will add it as a repository secret.

### Step 3 – Install the App on your repository

1. In the App settings, click **Install App**.
2. Select the account/organisation and choose the target repository.
3. Note the **Installation ID** from the URL: `https://github.com/settings/installations/INSTALLATION_ID`.

### Step 4 – Store credentials as repository secrets

Go to **Repository → Settings → Secrets and variables → Actions → New repository secret**:

| Secret name | Value |
|---|---|
| `GITHUB_APP_ID` | The numeric App ID from the App settings page |
| `GITHUB_APP_PRIVATE_KEY` | Contents of the downloaded `.pem` file (paste the full text, including headers) |
| `GITHUB_INSTALLATION_ID` | The installation ID from Step 3 |
| `GEMINI_API_KEY` | Your Google AI Studio API key |

> **Using a PAT instead of a GitHub App** — for a quick start you can use a personal access token with `repo` scope. Store it as `GITHUB_TOKEN` (or use the built-in `secrets.GITHUB_TOKEN`). The built-in token is sufficient for reviews and comments but **cannot merge PRs that require bypassing branch protection**.

---

## Gemini API configuration

1. Go to [Google AI Studio](https://aistudio.google.com/app/apikey) and create an API key.
2. Store the key as the `GEMINI_API_KEY` repository secret.
3. The default model is `gemini-1.5-pro-latest`. Override with the `GEMINI_MODEL` environment variable if needed.

Gemini API pricing and rate limits vary by model. The agent sends at most ~120 KB of diff per review. At current rates, a typical PR review costs a fraction of a cent.

---

## Environment variable reference

See [`.env.example`](.env.example) for the full list with descriptions. The most important variables:

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_TOKEN` | Yes* | — | PAT or Actions token |
| `GITHUB_APP_ID` | Yes* | — | GitHub App ID |
| `GITHUB_APP_PRIVATE_KEY` | Yes* | — | App private key (PEM) |
| `GITHUB_INSTALLATION_ID` | Yes* | — | App installation ID |
| `GITHUB_OWNER` | Yes | — | Repository owner |
| `GITHUB_REPO` | Yes | — | Repository name |
| `PR_NUMBER` | Yes | — | Pull request number |
| `GEMINI_API_KEY` | Yes | — | Gemini API key |
| `TARGET_BRANCH` | No | `main` | Branch PRs must target |
| `GEMINI_MODEL` | No | `gemini-1.5-pro-latest` | Gemini model name |
| `REQUIRED_CHECKS` | No | `build,vet,test` | Checks that must pass |
| `EXTRA_CHECK_COMMANDS` | No | — | Extra shell commands (`;`-separated) |
| `AI_REVIEW_POLICY` | No | `advisory` | `advisory` / `block_on_critical` / `block_on_any` |
| `AUTO_MERGE_ENABLED` | No | `false` | Enable automatic merging |
| `MERGE_STRATEGY` | No | `squash` | `merge` / `squash` / `rebase` |
| `DRY_RUN` | No | `false` | Post comments but never merge |
| `LOG_LEVEL` | No | `info` | `debug` / `info` / `warn` / `error` |
| `REQUEST_TIMEOUT_SECONDS` | No | `30` | API call timeout |

\* Provide either `GITHUB_TOKEN` **or** all three App credentials.

---

## GitHub Actions setup

The workflow file is at [`.github/workflows/code-review.yml`](.github/workflows/code-review.yml). It contains two jobs:

- **`go-checks`** — runs `go fmt`, `go vet`, `go build`, `go test` as individual steps so they appear as separate status checks.
- **`ai-review`** — builds the agent binary and runs the full review workflow after the checks pass.

To enable the workflow, push it to your repository's default branch. It will trigger automatically on every PR that targets `main`.

### Minimum required secrets

| Secret | Where to set |
|---|---|
| `GEMINI_API_KEY` | Repository → Settings → Secrets → Actions |

The built-in `GITHUB_TOKEN` is provided automatically by GitHub Actions and requires no setup.

### To use a GitHub App token instead of the built-in token

Add the App secrets (see [GitHub App creation](#github-app-creation-and-permissions)), then edit `.github/workflows/code-review.yml` — replace the `GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}` line with:

```yaml
GITHUB_TOKEN: ${{ secrets.AGENT_GITHUB_TOKEN }}
```

where `AGENT_GITHUB_TOKEN` stores the installation access token you generate via the App.

---

## Branch protection configuration

For the agent to be meaningful, configure branch protection on `main`:

1. Go to **Repository → Settings → Branches → Add branch ruleset** (or the classic branch protection page).
2. Apply to `main` and enable:
   - **Require a pull request before merging**
   - **Require status checks to pass** — add `Go checks / go-fmt`, `Go checks / go-vet`, `Go checks / go-build`, `Go checks / go-test`, and `AI review agent / AI review agent`
   - **Require branches to be up to date before merging**
   - **Do not allow bypassing the above settings** (prevents the agent from merging without passing checks)
3. Click **Save changes**.

> The agent deliberately does not bypass branch protection. If any required status check fails, the GitHub API will reject the merge request even when `AUTO_MERGE_ENABLED=true`.

---

## Running the agent locally

```bash
# 1. Copy and fill in the example environment file
cp .env.example .env
# Edit .env with your real values

# 2. Load environment variables (Linux/macOS)
export $(grep -v '^#' .env | xargs)

# Windows PowerShell equivalent
Get-Content .env | Where-Object { $_ -notmatch '^#' -and $_ -ne '' } |
  ForEach-Object { $k,$v = $_ -split '=',2; [System.Environment]::SetEnvironmentVariable($k,$v) }

# 3. Build and run
go build -o review-agent ./cmd/agent
./review-agent
```

The agent expects `PR_NUMBER` to point to an open PR in your configured repository. It will post a review comment on that PR.

### Dry-run mode

Set `DRY_RUN=true` to post no comments and never request a merge — useful for verifying configuration before going live:

```bash
DRY_RUN=true ./review-agent
```

---

## Testing with a sample pull request

### Quick smoke test

1. Create a test branch in your repository:
   ```bash
   git checkout -b test/agent-smoke-test
   echo "// smoke test" >> main.go
   git add main.go
   git commit -m "test: add smoke-test comment"
   git push origin test/agent-smoke-test
   ```

2. Open a PR from `test/agent-smoke-test` → `main` in the GitHub UI.

3. Watch the **Actions** tab — the `AI PR Review` workflow should start within seconds.

4. After the workflow completes, check the PR for:
   - A review comment from the agent with check results and AI review.
   - Status checks marked green or red.

### Integration test against a dedicated repository

For more thorough integration testing, create a separate `agent-test-repo` repository:

1. Push a Go module with intentional issues (e.g., a missing error check, an unsafe type assertion).
2. Configure the agent secrets in the test repository.
3. Open PRs with various code patterns and verify the AI review findings match expectations.
4. Test the merge path by setting `AUTO_MERGE_ENABLED=true` in a PR where all checks pass.

### Running unit tests

```bash
go test -v -race -count=1 ./...
```

All 47 tests should pass in under 10 seconds with no external API calls.

---

## Enabling automatic merging safely

Automatic merging is **disabled by default** (`AUTO_MERGE_ENABLED=false`). Follow this checklist before enabling it:

- [ ] Branch protection rules are configured and enforced (see [Branch protection](#branch-protection-configuration)).
- [ ] All required status checks are listed in the branch protection rules.
- [ ] You have validated the agent in dry-run mode (`DRY_RUN=true`) on several real PRs.
- [ ] The AI review policy is set to `advisory` or `block_on_critical` — `block_on_any` is overly strict for most teams and may block trivially.
- [ ] The repository has **auto-merge** enabled: Repository → Settings → Allow auto-merge.
- [ ] At least one human approval is required via branch protection (strongly recommended).

When you are satisfied, set `AUTO_MERGE_ENABLED=true` in the workflow environment:

```yaml
AUTO_MERGE_ENABLED: "true"
```

The agent will only merge when:

1. The PR is open and not a draft.
2. The PR targets `main` (or your configured `TARGET_BRANCH`).
3. All required automated checks have passed.
4. The AI review policy is satisfied.
5. The PR has no merge conflicts.
6. GitHub branch protection rules allow the merge (required approvals, required checks).
7. The head SHA has not changed since the review ran (prevents merging unreviewed pushes).

---

## Troubleshooting

### `configuration errors: GITHUB_TOKEN …`

You have not set the required environment variables. Copy `.env.example` to `.env`, fill in all required values, and re-run.

### `github api error 401`

The token is invalid or expired. For PATs, regenerate it. For GitHub App tokens, verify that `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, and `GITHUB_INSTALLATION_ID` are all correct and that the App is installed on the repository.

### `github api error 403: Resource not accessible by integration`

The token does not have sufficient permissions. If using the built-in `GITHUB_TOKEN`, check that the workflow `permissions` block includes `pull-requests: write` and `contents: write`. For merge operations that require bypassing branch protection, use a GitHub App or PAT with the necessary access.

### `gemini API error 400: API key not valid`

The `GEMINI_API_KEY` secret is missing or incorrect. Verify the key in [Google AI Studio](https://aistudio.google.com/app/apikey) and re-add it as a repository secret.

### `gemini API error 429` (rate limit)

The Gemini API has per-minute and per-day quotas. The agent retries with exponential back-off up to 3 times. If you are hitting sustained rate limits, switch to a paid Gemini tier or reduce the frequency of PR reviews.

### `merge blocked: merge not allowed by branch protection`

Branch protection rules are preventing the merge. This is intentional. Ensure:
- All required status checks have passed (green).
- The required number of human approvals has been met.
- The PR branch is up to date with `main`.

The agent cannot and will not bypass these rules. They are your safety net.

### `head SHA changed … aborting merge`

A new commit was pushed to the PR branch after the review started. The agent aborts to avoid merging unreviewed code. The workflow will re-trigger on the new push and review the updated commits automatically.

### AI review posts no comment / blank summary

The Gemini response was either empty or could not be parsed as valid JSON. Check the workflow logs for the raw Gemini response. If the model returned a refusal or non-JSON output, try switching to a different model via `GEMINI_MODEL`.

### Tests fail with `exit status 1` on Windows

The `TestRunner_ExtraCommandFail` test uses `exit 1` which is a Unix shell built-in. On Windows the test runner falls back to `cmd /C exit 1` which behaves correctly. If you see unexpected failures, ensure you are running `go test` from a standard Command Prompt or PowerShell rather than Git Bash.

### Checking agent logs

The agent writes structured JSON logs to stdout. In GitHub Actions they appear in the workflow run log. To filter for errors only:

```bash
LOG_LEVEL=error ./review-agent
```

To get full debug output including every API call:

```bash
LOG_LEVEL=debug ./review-agent
```
