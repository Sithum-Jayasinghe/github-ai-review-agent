// Package github wraps the GitHub REST API v3. All HTTP calls are made through
// a single Client so that authentication, base URL, timeouts, and retry logic
// live in one place.
package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	apiBase        = "https://api.github.com"
	acceptHeader   = "application/vnd.github+json"
	apiVersionHdr  = "2022-11-28"
	maxRetries     = 3
	retryBaseDelay = time.Second
)

// Client is a thin GitHub REST API client.
type Client struct {
	token      string
	httpClient *http.Client
	owner      string
	repo       string
}

// NewClientWithToken creates a Client authenticated with a personal access
// token or GitHub Actions GITHUB_TOKEN.
func NewClientWithToken(token, owner, repo string, timeout time.Duration) *Client {
	return &Client{
		token: token,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		owner: owner,
		repo:  repo,
	}
}

// NewClientWithApp creates a Client authenticated as a GitHub App installation.
// privateKeyPEM is the PEM-encoded RSA private key for the app.
func NewClientWithApp(
	appID, installationID int64,
	privateKeyPEM, owner, repo string,
	timeout time.Duration,
) (*Client, error) {
	token, err := generateInstallationToken(appID, installationID, privateKeyPEM, timeout)
	if err != nil {
		return nil, fmt.Errorf("github app auth: %w", err)
	}
	return NewClientWithToken(token, owner, repo, timeout), nil
}

// Owner returns the configured repository owner.
func (c *Client) Owner() string { return c.owner }

// Repo returns the configured repository name.
func (c *Client) Repo() string { return c.repo }

// do executes an authenticated HTTP request and decodes the JSON response into
// out (if out is non-nil). It retries on transient errors and rate-limit
// responses.
func (c *Client) do(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(math.Pow(2, float64(attempt-1))) * retryBaseDelay
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		err := c.doOnce(ctx, method, path, body, out)
		if err == nil {
			return nil
		}

		var apiErr *APIError
		if asAPIError(err, &apiErr) {
			if apiErr.StatusCode == http.StatusUnauthorized ||
				apiErr.StatusCode == http.StatusForbidden ||
				apiErr.StatusCode == http.StatusNotFound ||
				apiErr.StatusCode == http.StatusUnprocessableEntity {
				// Non-retryable client errors.
				return err
			}
			if apiErr.StatusCode == http.StatusTooManyRequests ||
				apiErr.StatusCode == http.StatusForbidden {
				// Rate-limited – honour Retry-After if present.
				if apiErr.RetryAfter > 0 {
					select {
					case <-time.After(apiErr.RetryAfter):
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
		}
		lastErr = err
	}
	return fmt.Errorf("after %d retries: %w", maxRetries, lastErr)
}

func (c *Client) doOnce(ctx context.Context, method, path string, body, out interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	url := apiBase + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("X-GitHub-Api-Version", apiVersionHdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request to %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{
			StatusCode: resp.StatusCode,
			URL:        url,
			Body:       string(respBody),
		}
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				apiErr.RetryAfter = time.Duration(secs) * time.Second
			}
		}
		// Try to extract GitHub's message field.
		var ghErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &ghErr)
		apiErr.Message = ghErr.Message
		return apiErr
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response from %s: %w", url, err)
		}
	}
	return nil
}

// APIError represents an error returned by the GitHub API.
type APIError struct {
	StatusCode int
	URL        string
	Message    string
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("github api error %d on %s: %s", e.StatusCode, e.URL, e.Message)
	}
	return fmt.Sprintf("github api error %d on %s", e.StatusCode, e.URL)
}

// asAPIError is a helper analogous to errors.As for *APIError.
func asAPIError(err error, target **APIError) bool {
	if err == nil {
		return false
	}
	if ae, ok := err.(*APIError); ok {
		*target = ae
		return true
	}
	return false
}

// IsNotFound returns true when the error is a 404 from GitHub.
func IsNotFound(err error) bool {
	var ae *APIError
	return asAPIError(err, &ae) && ae.StatusCode == http.StatusNotFound
}

// IsConflict returns true when GitHub returns 409 (merge conflict).
func IsConflict(err error) bool {
	var ae *APIError
	return asAPIError(err, &ae) && ae.StatusCode == http.StatusConflict
}

// -----------------------------------------------------------------------
// GitHub App JWT helpers
// -----------------------------------------------------------------------

func generateInstallationToken(
	appID, installationID int64,
	privateKeyPEM string,
	timeout time.Duration,
) (string, error) {
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}

	appJWT, err := buildAppJWT(appID, privateKey)
	if err != nil {
		return "", err
	}

	// Exchange the App JWT for an installation access token.
	httpClient := &http.Client{Timeout: timeout}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", apiBase, installationID)

	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return "", fmt.Errorf("create installation token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("X-GitHub-Api-Version", apiVersionHdr)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("installation token request failed (%d): %s", resp.StatusCode, body)
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode installation token: %w", err)
	}
	return result.Token, nil
}

func buildAppJWT(appID int64, key *rsa.PrivateKey) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Add(-60 * time.Second).Unix(), // issued-at with 60 s skew allowance
		"exp": now.Add(9 * time.Minute).Unix(),   // max 10 minutes
		"iss": strconv.FormatInt(appID, 10),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(key)
}
