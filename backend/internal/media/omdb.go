package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// omdbClient bounds every OMDB call; these run once per search result, serially.
var omdbClient = &http.Client{Timeout: 10 * time.Second}

type omdbResponse struct {
	Actors     string `json:"Actors"`
	ImdbRating string `json:"imdbRating"`
	Response   string `json:"Response"`
}

// omdbStats returns the IMDb rating and actors for a movie title. With no
// API key configured it returns an error, which callers treat as non-fatal.
func omdbStats(ctx context.Context, apiKey, title string) (float64, []string, error) {
	if apiKey == "" {
		return 0, nil, errors.New("OMDB_API_KEY is not set")
	}

	q := url.Values{}
	q.Set("apikey", apiKey)
	q.Set("type", "movie")
	q.Set("plot", "short")
	q.Set("t", title)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.omdbapi.com/?"+q.Encode(), nil)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to build OMDB request: %w", err)
	}
	resp, err := omdbClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to fetch OMDB data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, nil, fmt.Errorf("OMDB API returned status code %d", resp.StatusCode)
	}

	var body omdbResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, nil, fmt.Errorf("failed to decode OMDB response: %w", err)
	}
	if body.Response != "True" {
		return 0, nil, errors.New("movie not found")
	}

	actors := strings.Split(body.Actors, ", ")
	rating, err := strconv.ParseFloat(body.ImdbRating, 64)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to parse IMDb rating: %w", err)
	}
	return rating, actors, nil
}
