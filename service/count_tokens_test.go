package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"codebuddy-gateway/global"
)

type countTokensRoundTripper func(*http.Request) (*http.Response, error)

func (f countTokensRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestCountTokensUpstreamReadsPromptUsage(t *testing.T) {
	defer setupRotatorDB(t)()
	addRotatorAccount(t, "count", "jwt-count", 20, true, 0)
	oldGateway := global.CORE_CONFIG.Gateway
	defer func() { global.CORE_CONFIG.Gateway = oldGateway }()
	global.CORE_CONFIG.Gateway.MaxRetries = 1
	global.CORE_CONFIG.Gateway.Upstream = "https://upstream.test"

	client := NewUpstreamClient()
	client.httpClient.Transport = countTokensRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v2/chat/completions" {
			t.Fatalf("path=%s", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"max_tokens":1`) || !strings.Contains(string(body), `"stream":true`) {
			t.Fatalf("body=%s", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`data: {"choices":[],"usage":{"prompt_tokens":123,"completion_tokens":1}}

data: [DONE]

`)),
			Header:  make(http.Header),
			Request: req,
		}, nil
	})
	p := &Proxy{client: client, rotator: NewRotator(), refresher: NewRefresher(client)}
	got, err := p.countTokensUpstream(context.Background(), http.Header{"X-Session-Id": []string{"count-test"}}, []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != 123 {
		t.Fatalf("tokens=%d", got)
	}
}
