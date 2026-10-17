package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"
)

const zenEndpoint = "https://opencode.ai/zen/v1/systemone"
const effortInstructions = "How much reasoning effort should a coding agent spend on this user message, given the assistant's previous reply?"

func effortCriteria() map[string]string {
	return map[string]string{
		"low":    "Trivial: a quick question, confirmation, chit-chat, running one command, or a tiny obvious edit.",
		"medium": "Routine: a clear, well-scoped coding task or explanation with obvious steps.",
		"high":   "Substantial: multi-step engineering, debugging, or design that needs careful reasoning.",
		"xhigh":  "Hard: open-ended architecture, subtle debugging, research, or a long autonomous build.",
	}
}

func newJevServer(endpoint, model, apiKey string, timeout time.Duration, logger *log.Logger) (*server, error) {
	// Production egress is pinned to Zen. Literal loopback HTTP is allowed for
	// contract tests/local Jev servers; never follow redirects or environment proxies.
	if endpoint != zenEndpoint {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1/systemone" {
			return nil, errors.New("invalid Jev endpoint")
		}
		if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
			return nil, errors.New("invalid Jev endpoint")
		}
	}
	if model != "jev-1.13-free" && model != "jev-1.13" && model != "jev-latest" {
		return nil, errors.New("invalid Jev model")
	}
	// Share the bounded, proxy-free client validation with the OCG implementation.
	s, err := newServer("http://127.0.0.1", "ocg/unused", apiKey, timeout, logger)
	if err != nil {
		return nil, err
	}
	s.endpoint, s.model, s.protocol = endpoint, model, "jev"
	return s, nil
}

func (s *server) classifyJev(ctx context.Context, in jevRequest) (string, tokenUsage, string) {
	// Internal turn ID and shadow marker are local metadata, not model input.
	body, _ := json.Marshal(map[string]any{
		"model":     s.model,
		"state":     map[string]string{"assistant_previous_reply_tail": in.State.Previous, "user_message": in.State.Message},
		"questions": map[string]any{"effort": map[string]any{"type": "choice", "instructions": effortInstructions, "criteria": effortCriteria()}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", tokenUsage{}, "request"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return "", tokenUsage{}, "timeout"
		}
		return "", tokenUsage{}, "transport"
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", tokenUsage{}, fmt.Sprintf("http_%d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024+1))
	if err != nil {
		return "", tokenUsage{}, "read"
	}
	if len(raw) > 16*1024 {
		return "", tokenUsage{}, "oversize"
	}
	var result struct {
		Answers struct {
			Effort struct {
				Choice string `json:"choice"`
			} `json:"effort"`
		} `json:"answers"`
		Usage tokenUsage `json:"usage"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return "", tokenUsage{}, "json"
	}
	result.Usage.Available = result.Usage.Input > 0 || result.Usage.Output > 0
	for _, choice := range choices {
		if result.Answers.Effort.Choice == choice {
			return choice, result.Usage, "ok"
		}
	}
	return "", result.Usage, "answer"
}

type observation struct {
	Model   string     `json:"model"`
	Effort  string     `json:"effort,omitempty"`
	Outcome string     `json:"outcome"`
	MS      int64      `json:"ms"`
	Usage   tokenUsage `json:"usage"`
}

func (s *server) observe(ctx context.Context, in jevRequest, session string) (o observation) {
	start := time.Now()
	o.Model = s.model
	defer func() {
		if recover() != nil {
			o.Effort = ""
			o.Outcome = "panic"
		}
		o.MS = time.Since(start).Milliseconds()
	}()
	o.Effort, o.Usage, o.Outcome = s.classify(ctx, in, session)
	return o
}

func (s *server) evaluate(ctx context.Context, in jevRequest, session string) (observation, *observation) {
	if s.comparison == nil {
		return s.observe(ctx, in, session), nil
	}
	// Join both calls under the same deadline. A comparator never supplies the
	// response choice and never becomes a fallback when Jev fails.
	primary := make(chan observation, 1)
	comparison := make(chan observation, 1)
	go func() { primary <- s.observe(ctx, in, session) }()
	go func() { comparison <- s.comparison.observe(ctx, in, session) }()
	p, c := <-primary, <-comparison
	return p, &c
}
