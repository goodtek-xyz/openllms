package proxy

import "net/http"

// NewUpstreamClient is the HTTP client for calls to LLM providers. It sets no Client.Timeout: net/http counts reading
// the response body in it ("The timeout includes connection time, any redirects, and reading the response body",
// https://pkg.go.dev/net/http#Client), so any fixed value cuts streams that run longer. The total time belongs to the
// caller: the upstream request carries the client's request context, so the call ends when the client goes away, and
// client SDKs bound it themselves (Anthropic SDKs: "By default requests time out after 10 minutes", with streaming
// advised for longer requests — https://platform.claude.com/docs/en/cli-sdks-libraries/sdks/python#timeouts).
// Connection setup and dead-peer detection keep Go's DefaultTransport defaults (dial 30 s, TCP keep-alive 30 s, TLS
// handshake 10 s, idle connections 90 s; https://pkg.go.dev/net/http#DefaultTransport).
func NewUpstreamClient() *http.Client {
	return &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
}
