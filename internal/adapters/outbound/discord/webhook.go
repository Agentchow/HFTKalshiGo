package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/charleschow/hft-trading/internal/telemetry"
)

type Notifier struct {
	webhookURL string
	httpClient *http.Client
}

func NewNotifier(webhookURL string) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (n *Notifier) Enabled() bool { return n.webhookURL != "" }

type Embed struct {
	Title       string  `json:"title,omitempty"`
	Description string  `json:"description,omitempty"`
	Color       int     `json:"color,omitempty"`
	Fields      []Field `json:"fields,omitempty"`
	Timestamp   string  `json:"timestamp,omitempty"`
}

type Field struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type webhookPayload struct {
	Content string  `json:"content,omitempty"`
	Embeds  []Embed `json:"embeds,omitempty"`
}

func (n *Notifier) SendText(ctx context.Context, msg string) error {
	return n.send(ctx, webhookPayload{Content: msg})
}

func (n *Notifier) SendEmbed(ctx context.Context, embed Embed) error {
	if embed.Timestamp == "" {
		embed.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	return n.send(ctx, webhookPayload{Embeds: []Embed{embed}})
}

func (n *Notifier) send(ctx context.Context, payload webhookPayload) error {
	if !n.Enabled() {
		return nil
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal discord payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("discord webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		telemetry.Warnf("discord: rate limited")
		return fmt.Errorf("discord rate limited")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook: status=%d", resp.StatusCode)
	}

	return nil
}

// --- Convenience methods for common alert types ---

const (
	ColorGreen  = 0x2ECC71
	ColorRed    = 0xE74C3C
	ColorYellow = 0xF1C40F
	ColorBlue   = 0x3498DB
)

func (n *Notifier) EdgeAlert(ctx context.Context, sport, eid, outcome string, modelPct, kalshiPct, diff float64) error {
	return n.SendEmbed(ctx, Embed{
		Title: fmt.Sprintf("Edge Detected — %s", sport),
		Color: ColorGreen,
		Fields: []Field{
			{Name: "Game", Value: eid, Inline: true},
			{Name: "Outcome", Value: outcome, Inline: true},
			{Name: "Model", Value: fmt.Sprintf("%.1f%%", modelPct), Inline: true},
			{Name: "Kalshi", Value: fmt.Sprintf("%.0f¢", kalshiPct), Inline: true},
			{Name: "Edge", Value: fmt.Sprintf("+%.1f%%", diff), Inline: true},
		},
	})
}

func (n *Notifier) OrderFill(ctx context.Context, ticker, orderID, side string, priceCents int) error {
	return n.SendEmbed(ctx, Embed{
		Title: "Order Filled",
		Color: ColorBlue,
		Fields: []Field{
			{Name: "Ticker", Value: ticker, Inline: true},
			{Name: "Side", Value: side, Inline: true},
			{Name: "Price", Value: fmt.Sprintf("%d¢", priceCents), Inline: true},
			{Name: "Order ID", Value: orderID, Inline: false},
		},
	})
}

func (n *Notifier) GameOver(ctx context.Context, sport, eid, homeTeam, awayTeam string, homeScore, awayScore int) error {
	return n.SendEmbed(ctx, Embed{
		Title:       fmt.Sprintf("Game Over — %s", sport),
		Description: fmt.Sprintf("%s %d – %d %s", homeTeam, homeScore, awayScore, awayTeam),
		Color:       ColorYellow,
	})
}
