package goalserve

import (
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/charleschow/hft-trading/internal/telemetry"
)

type Client struct {
	apiKey     string
	httpClient *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *Client) feedURL(sport, league, feed string) string {
	return fmt.Sprintf("http://www.goalserve.com/getfeed/%s/%s/%s/%s",
		c.apiKey, sport, league, feed)
}

func (c *Client) oddsURL(sport, league, cat string) string {
	return fmt.Sprintf("http://www.goalserve.com/getfeed/%s/%s/%s/%s",
		c.apiKey, sport, league, cat)
}

func (c *Client) inplayURL(sport string) string {
	return fmt.Sprintf("http://inplay.goalserve.com/getfeed/%s/%s/inplay",
		c.apiKey, sport)
}

func (c *Client) fetchXML(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Accept-Encoding", "gzip")

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		reader = gz
	}

	if err := xml.NewDecoder(reader).Decode(out); err != nil {
		return fmt.Errorf("xml decode: %w", err)
	}

	telemetry.Debugf("goalserve: GET %s -> %d (%s)", url, resp.StatusCode, time.Since(start))
	return nil
}
