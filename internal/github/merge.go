package github

import (
	"context"
	"fmt"
	"net/http"
)

// MergeResult is returned after a successful merge request.
type MergeResult struct {
	SHA     string `json:"sha"`
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

// MergeRequest is the payload sent to the GitHub merge endpoint.
type MergeRequest struct {
	// CommitTitle is the title of the resulting merge commit.
	CommitTitle string `json:"commit_title,omitempty"`
	// CommitMessage is the body of the resulting merge commit.
	CommitMessage string `json:"commit_message,omitempty"`
	// SHA is the head commit SHA that must match to prevent accidental merges.
	SHA string `json:"sha"`
	// MergeMethod is "merge", "squash", or "rebase".
	MergeMethod string `json:"merge_method,omitempty"`
}

// MergePullRequest requests a merge of the given pull request. The SHA field
// in the request MUST match the current head to prevent race conditions.
//
// Returns ErrMergeConflict when GitHub reports a conflict or
// ErrMergeNotAllowed when branch protection rules block the merge.
func (c *Client) MergePullRequest(ctx context.Context, number int, req MergeRequest) (*MergeResult, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", c.owner, c.repo, number)
	var result MergeResult
	err := c.do(ctx, http.MethodPut, path, req, &result)
	if err != nil {
		if IsConflict(err) {
			return nil, fmt.Errorf("%w: %v", ErrMergeConflict, err)
		}
		var apiErr *APIError
		if asAPIError(err, &apiErr) && apiErr.StatusCode == http.StatusMethodNotAllowed {
			return nil, fmt.Errorf("%w: %v", ErrMergeNotAllowed, err)
		}
		return nil, fmt.Errorf("merge pull request #%d: %w", number, err)
	}
	return &result, nil
}

// EnableAutoMerge enables GitHub's native auto-merge feature on the pull
// request so GitHub will merge it automatically once all checks pass. This is
// a GraphQL-only operation; we call the v4 API via REST proxy endpoint.
//
// Note: auto-merge must be enabled in the repository settings first.
func (c *Client) EnableAutoMerge(ctx context.Context, prNodeID, mergeMethod string) error {
	// GitHub's auto-merge is only available through the GraphQL API.
	mutation := fmt.Sprintf(`mutation {
  enablePullRequestAutoMerge(input: {
    pullRequestId: %q,
    mergeMethod: %s
  }) {
    pullRequest {
      autoMergeRequest {
        enabledAt
      }
    }
  }
}`, prNodeID, toGraphQLMergeMethod(mergeMethod))

	payload := map[string]string{"query": mutation}
	var result map[string]interface{}
	if err := c.do(ctx, http.MethodPost, "/graphql", payload, &result); err != nil {
		return fmt.Errorf("enable auto-merge: %w", err)
	}
	if errs, ok := result["errors"]; ok {
		return fmt.Errorf("enable auto-merge graphql errors: %v", errs)
	}
	return nil
}

// toGraphQLMergeMethod converts a REST merge method string to the GraphQL
// enum value expected by the enablePullRequestAutoMerge mutation.
func toGraphQLMergeMethod(method string) string {
	switch method {
	case "squash":
		return "SQUASH"
	case "rebase":
		return "REBASE"
	default:
		return "MERGE"
	}
}

// Sentinel errors -------------------------------------------------------

// ErrMergeConflict is returned when the pull request has unresolved conflicts.
var ErrMergeConflict = fmt.Errorf("merge conflict")

// ErrMergeNotAllowed is returned when branch protection rules prevent merging.
var ErrMergeNotAllowed = fmt.Errorf("merge not allowed by branch protection")
