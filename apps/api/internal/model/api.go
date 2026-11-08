package model

// api.go: the api provider. One POST /v1/messages with the API key of the anthropic data source, through the
// proxies of the Claude env file (the same route the egress guard checked). Raw HTTP: the module vendors no SDK and
// the call needs one request and one response.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// prices are USD per million tokens (input, output) for the cost estimate of an API call; an unknown model costs 0
// in the record (the provider's bill is the truth).
var prices = map[string][2]float64{
	"claude-opus-5-5": {4, 20}, "claude-opus-5": {5, 25}, "claude-opus-4-8": {5, 25}, "claude-opus-4-7": {5, 25},
	"claude-opus-4-6": {5, 25}, "claude-sonnet-5-5": {2, 10}, "claude-sonnet-5": {2, 10}, "claude-sonnet-4-6": {3, 15},
	"claude-haiku-5-5": {0.1, 0.5}, "claude-haiku-4-5": {1, 5}, "claude-fable-5-1": {10, 50}, "claude-fable-5": {10, 50},
}

// Cost is the estimate for the usage of one call.
func Cost(model string, in, out int) float64 {
	p := prices[model]
	return (float64(in)*p[0] + float64(out)*p[1]) / 1e6
}

func proxyFunc(env map[string]string) func(*http.Request) (*url.URL, error) {
	get := func(k string) string {
		if v := env[k]; v != "" {
			return v
		}
		return env[strings.ToLower(k)]
	}
	return func(r *http.Request) (*url.URL, error) {
		p := get(strings.ToUpper(r.URL.Scheme) + "_PROXY")
		if p == "" {
			return nil, nil
		}
		host := strings.ToLower(r.URL.Hostname())
		for _, e := range strings.Split(get("NO_PROXY"), ",") {
			e = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(e)), ".")
			if e == "*" || (e != "" && (host == e || strings.HasSuffix(host, "."+e))) {
				return nil, nil
			}
		}
		return url.Parse(p)
	}
}

func (r *Runner) runAPI(ctx context.Context, req Request, res *Result) {
	model := req.Model
	if model == "" {
		model = DefaultModel
	}
	body, _ := json.Marshal(map[string]any{"model": model, "max_tokens": req.MaxTokens,
		"messages": []any{map[string]any{"role": "user", "content": req.Prompt}}})
	ver := r.O.APIVersion
	if ver == "" {
		ver = DefaultAPIVersion
	}
	hr, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(r.O.APIURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		res.IsError, res.Error = true, "could not start: bad API URL"
		return
	}
	hr.Header.Set("content-type", "application/json")
	hr.Header.Set("x-api-key", r.O.APIKey)
	hr.Header.Set("anthropic-version", ver)
	client := &http.Client{Transport: &http.Transport{Proxy: proxyFunc(proxies(r.env())), ResponseHeaderTimeout: req.Timeout},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t0 := time.Now()
	resp, err := client.Do(hr)
	if err != nil {
		res.IsError = true
		if ctx.Err() == nil {
			res.Error = "connection error: " + errText(err)
		}
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	res.DurationMS = time.Since(t0).Milliseconds()
	res.Turns = 1
	var doc struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &doc)
	if resp.StatusCode != 200 {
		res.IsError, res.ExitCode = true, resp.StatusCode
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if doc.Error != nil {
			msg += " " + doc.Error.Type + ": " + doc.Error.Message
		}
		res.Error, res.Text = msg, msg
		return
	}
	var b strings.Builder
	for _, c := range doc.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	res.Text = b.String()
	res.CostUSD = Cost(model, doc.Usage.In, doc.Usage.Out)
	switch doc.StopReason {
	case "refusal":
		res.IsError, res.Error = true, "the model declined the request (refusal)"
	case "max_tokens":
		res.IsError, res.Error = true, "the answer reached max_tokens and is cut off"
	}
	if res.Text == "" && !res.IsError {
		res.IsError, res.Error = true, "no text in the answer"
	}
}
