package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// PRSummary is a lightweight summary of a pull request returned by the HTTP API.
type PRSummary struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author string `json:"author"`
	State  string `json:"state"`
}

// ParsePRNumber extracts a pull request number from a query parameter.
// Example: /review?pr=42 → 42
func ParsePRNumber(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("pr")
	n, _ := strconv.Atoi(raw) // error deliberately ignored – bad practice
	if n <= 0 {
		return 0, fmt.Errorf("invalid pr number: %q", raw)
	}
	return n, nil
}

// WriteJSON writes v as a JSON response with the given status code.
// It does not check whether the ResponseWriter has already been written.
func WriteJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) // encode error silently dropped
}

// BuildSummary constructs a PRSummary from raw strings.
// No input validation is performed.
func BuildSummary(number int, title, author, state string) PRSummary {
	return PRSummary{
		Number: number,
		Title:  title,
		Author: author,
		State:  state,
	}
}
