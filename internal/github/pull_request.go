package github

import (
	"context"
	"fmt"
	"net/http"
)

// PullRequest holds the fields we need from the GitHub PR object.
type PullRequest struct {
	Number    int    `json:"number"`
	State     string `json:"state"` // "open" | "closed"
	Title     string `json:"title"`
	Body      string `json:"body"`
	Mergeable *bool  `json:"mergeable"` // null while GitHub is computing
	Draft     bool   `json:"draft"`

	Head struct {
		Ref string `json:"ref"` // source branch name
		SHA string `json:"sha"` // latest commit SHA
	} `json:"head"`

	Base struct {
		Ref string `json:"ref"` // target branch name
	} `json:"base"`

	User struct {
		Login string `json:"login"`
	} `json:"user"`

	HTMLURL string `json:"html_url"`

	// MergeableState may be: "clean", "blocked", "behind", "dirty",
	// "draft", "has_hooks", "unknown".
	MergeableState string `json:"mergeable_state"`
}

// File represents one changed file in a pull request.
type File struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"` // added | modified | removed | renamed
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Changes   int    `json:"changes"`
	Patch     string `json:"patch"` // unified diff
	BlobURL   string `json:"blob_url"`
}

// Review represents a submitted pull-request review.
type Review struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	// APPROVE | REQUEST_CHANGES | COMMENT
	State string `json:"state"`
	User  struct {
		Login string `json:"login"`
	} `json:"user"`
}

// ReviewRequest is the payload for submitting a PR review.
type ReviewRequest struct {
	CommitID string         `json:"commit_id"`
	Body     string         `json:"body"`
	Event    string         `json:"event"` // APPROVE | REQUEST_CHANGES | COMMENT
	Comments []ReviewComment `json:"comments,omitempty"`
}

// ReviewComment is an inline code comment on a specific file and line.
type ReviewComment struct {
	Path     string `json:"path"`
	Position int    `json:"position,omitempty"` // position in the diff
	Line     int    `json:"line,omitempty"`     // line in the file (alternative to position)
	Body     string `json:"body"`
}

// IssueComment is a top-level comment on a pull request / issue.
type IssueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

// CheckRun represents a GitHub Actions check run.
type CheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`     // queued | in_progress | completed
	Conclusion string `json:"conclusion"` // success | failure | neutral | cancelled | skipped | timed_out | action_required
	HTMLURL    string `json:"html_url"`
}

// GetPullRequest fetches a single PR by number.
func (c *Client) GetPullRequest(ctx context.Context, number int) (*PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", c.owner, c.repo, number)
	var pr PullRequest
	if err := c.do(ctx, http.MethodGet, path, nil, &pr); err != nil {
		return nil, fmt.Errorf("get pull request #%d: %w", number, err)
	}
	return &pr, nil
}

// ListPullRequestFiles returns every changed file for a PR (handles pagination).
func (c *Client) ListPullRequestFiles(ctx context.Context, number int) ([]File, error) {
	var all []File
	page := 1
	for {
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100&page=%d",
			c.owner, c.repo, number, page)
		var files []File
		if err := c.do(ctx, http.MethodGet, path, nil, &files); err != nil {
			return nil, fmt.Errorf("list pr files page %d: %w", page, err)
		}
		all = append(all, files...)
		if len(files) < 100 {
			break
		}
		page++
	}
	return all, nil
}

// ListPullRequestReviews returns all reviews for a PR.
func (c *Client) ListPullRequestReviews(ctx context.Context, number int) ([]Review, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100", c.owner, c.repo, number)
	var reviews []Review
	if err := c.do(ctx, http.MethodGet, path, nil, &reviews); err != nil {
		return nil, fmt.Errorf("list pr reviews: %w", err)
	}
	return reviews, nil
}

// CreateReview submits a new review on the pull request.
func (c *Client) CreateReview(ctx context.Context, number int, req ReviewRequest) (*Review, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", c.owner, c.repo, number)
	var review Review
	if err := c.do(ctx, http.MethodPost, path, req, &review); err != nil {
		return nil, fmt.Errorf("create review: %w", err)
	}
	return &review, nil
}

// CreateIssueComment posts a top-level comment on the PR/issue.
func (c *Client) CreateIssueComment(ctx context.Context, number int, body string) (*IssueComment, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", c.owner, c.repo, number)
	payload := map[string]string{"body": body}
	var comment IssueComment
	if err := c.do(ctx, http.MethodPost, path, payload, &comment); err != nil {
		return nil, fmt.Errorf("create issue comment: %w", err)
	}
	return &comment, nil
}

// ListCheckRunsForRef returns check runs for a specific commit SHA.
func (c *Client) ListCheckRunsForRef(ctx context.Context, ref string) ([]CheckRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100", c.owner, c.repo, ref)
	var result struct {
		CheckRuns []CheckRun `json:"check_runs"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("list check runs for %s: %w", ref, err)
	}
	return result.CheckRuns, nil
}

// GetRepository returns basic repository metadata.
func (c *Client) GetRepository(ctx context.Context) (*Repository, error) {
	path := fmt.Sprintf("/repos/%s/%s", c.owner, c.repo)
	var repo Repository
	if err := c.do(ctx, http.MethodGet, path, nil, &repo); err != nil {
		return nil, fmt.Errorf("get repository: %w", err)
	}
	return &repo, nil
}

// Repository holds the subset of GitHub repo fields we care about.
type Repository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
}

// AddLabel attaches a label to a PR / issue. Existing labels are preserved.
func (c *Client) AddLabel(ctx context.Context, number int, label string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", c.owner, c.repo, number)
	payload := map[string][]string{"labels": {label}}
	if err := c.do(ctx, http.MethodPost, path, payload, nil); err != nil {
		return fmt.Errorf("add label %q: %w", label, err)
	}
	return nil
}

// RemoveLabel detaches a label from a PR / issue. Returns nil if label was
// already absent.
func (c *Client) RemoveLabel(ctx context.Context, number int, label string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels/%s", c.owner, c.repo, number, label)
	err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if err != nil && IsNotFound(err) {
		return nil // already removed
	}
	if err != nil {
		return fmt.Errorf("remove label %q: %w", label, err)
	}
	return nil
}
