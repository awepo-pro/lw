// Package web provides the search-provider seam behind the web.search
// tool: the SearchProvider interface and the one built-in implementation,
// Tavily. It stands alone — no logging, config or tools imports (contract
// §2) — and never touches the vault.
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// defaultEndpoint is the production Tavily search URL, used when
// Tavily.Endpoint is empty.
const defaultEndpoint = "https://api.tavily.com/search"

// defaultMaxResults is the hit cap applied when a caller passes max <= 0.
const defaultMaxResults = 5

// SearchHit is one web search result. Snippet carries the provider's
// content field: a short extract of the page, not the page itself.
type SearchHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// SearchProvider queries the web on behalf of the web.search tool. A nil
// provider means the tool is not offered at all (tools registry rule).
type SearchProvider interface {
	Search(ctx context.Context, query string, max int) ([]SearchHit, error)
}

// Tavily is the built-in SearchProvider, backed by api.tavily.com.
type Tavily struct {
	// Endpoint is the search URL; empty means defaultEndpoint.
	Endpoint string
	// APIKey is sent as the Bearer credential on every search.
	APIKey string
	// HTTP makes the request; nil means http.DefaultClient.
	HTTP *http.Client
}

// NewTavily returns a Tavily provider speaking to the production endpoint.
func NewTavily(apiKey string, hc *http.Client) *Tavily {
	return &Tavily{APIKey: apiKey, HTTP: hc}
}

// WithEndpoint points the provider at e and returns it — a test seam, so
// tests can dial an httptest server instead of the network.
func (t *Tavily) WithEndpoint(e string) *Tavily {
	t.Endpoint = e
	return t
}

// tavilyRequest is the JSON body the Tavily search endpoint accepts.
type tavilyRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

// tavilyResponse is the part of Tavily's reply we need; the decoder
// ignores every other field (score, answer, raw_content, …).
type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

// Search runs one Tavily query and returns at most max hits, in the order
// the provider returned them; max <= 0 means defaultMaxResults. Snippet
// is mapped from the provider's content field. Errors carry the `web:`
// prefix (contract §2).
func (t *Tavily) Search(ctx context.Context, query string, max int) ([]SearchHit, error) {
	if max <= 0 {
		max = defaultMaxResults
	}
	ep := t.Endpoint
	if ep == "" {
		ep = defaultEndpoint
	}
	body, err := json.Marshal(tavilyRequest{Query: query, MaxResults: max})
	if err != nil {
		return nil, fmt.Errorf("web: tavily request: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("web: tavily request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.APIKey)
	hc := t.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web: tavily request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web: tavily search failed: %s", strconv.Itoa(resp.StatusCode))
	}
	var tr tavilyResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("web: tavily decode: %v", err)
	}
	hits := make([]SearchHit, 0, len(tr.Results))
	for _, r := range tr.Results {
		hits = append(hits, SearchHit{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	if len(hits) > max {
		hits = hits[:max]
	}
	return hits, nil
}
