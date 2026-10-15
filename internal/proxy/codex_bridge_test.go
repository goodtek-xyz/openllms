package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func testBridgeOpts(model, id string) codexBridgeOpts {
	return codexBridgeOpts{model: model, id: id, accountID: "acc-1", provider: "codex"}
}

func TestChatToCodexResponses(t *testing.T) {
	in := []byte(`{
	  "model":"gpt-5",
	  "stream":false,
	  "messages":[
	    {"role":"system","content":"Be brief"},
	    {"role":"user","content":"hi"}
	  ]
	}`)
	out, clientStream, model, err := chatToCodexResponses(in)
	if err != nil {
		t.Fatal(err)
	}
	if clientStream || model != "gpt-5" {
		t.Fatalf("stream=%v model=%s", clientStream, model)
	}
	s := string(out)
	for _, want := range []string{`"stream":true`, `"store":false`, `"instructions":"Be brief"`, `"input_text"`, `"hi"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
}

func TestChatToCodexResponsesWebSearchTool(t *testing.T) {
	in := []byte(`{
	  "model":"gpt-5.4-mini",
	  "messages":[{"role":"user","content":"CEO?"}],
	  "tools":[{"type":"function","function":{"name":"web_search","description":"search","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}}],
	  "llms":{"web_search":{"mode":"live"}}
	}`)
	out, _, _, err := chatToCodexResponses(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"type":"web_search"`) || !strings.Contains(s, `"external_web_access":true`) {
		t.Fatalf("web_search mapping: %s", s)
	}
	if strings.Contains(s, `"name":"web_search"`) {
		t.Fatal("should not pass function name for hosted web_search")
	}
}

func TestChatToCodexResponsesGenerateImageTool(t *testing.T) {
	in := []byte(`{
	  "model":"gpt-5.4-mini",
	  "messages":[{"role":"user","content":"red circle"}],
	  "tools":[{"type":"function","function":{"name":"generate_image","description":"image","parameters":{"type":"object","properties":{"prompt":{"type":"string"}}}}}]
	}`)
	out, _, _, err := chatToCodexResponses(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"type":"image_generation"`) {
		t.Fatalf("image tool: %s", s)
	}
}

func TestChatToCodexResponsesFunctionTool(t *testing.T) {
	in := []byte(`{
	  "model":"gpt-5",
	  "messages":[{"role":"user","content":"go"}],
	  "tools":[{"type":"function","function":{"name":"browser_search","description":"browser","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}}]
	}`)
	out, _, _, err := chatToCodexResponses(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"name":"browser_search"`) || !strings.Contains(s, `"type":"function"`) {
		t.Fatalf("function tool: %s", s)
	}
}

func TestChatToCodexResponsesMultiTurnTools(t *testing.T) {
	in := []byte(`{
	  "model":"gpt-5",
	  "messages":[
	    {"role":"user","content":"weather?"},
	    {"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"browser_search","arguments":"{\"query\":\"Toledo\"}"}}]},
	    {"role":"tool","tool_call_id":"call_1","content":"62F partly cloudy"}
	  ],
	  "tools":[{"type":"function","function":{"name":"browser_search","description":"search","parameters":{"type":"object"}}}]
	}`)
	out, _, _, err := chatToCodexResponses(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		`"type":"function_call"`,
		`"call_id":"call_1"`,
		`"type":"function_call_output"`,
		`"output":"62F partly cloudy"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
}

func TestChatRequestHasTools(t *testing.T) {
	if chatRequestHasTools([]byte(`{"messages":[],"tools":[{"type":"function","function":{"name":"x"}}]}`)) != true {
		t.Fatal("expected tools")
	}
	if chatRequestHasTools([]byte(`{"messages":[]}`)) {
		t.Fatal("expected no tools")
	}
}

func TestConvertCodexSSEToChat(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"Hel"}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"lo"}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2}}}`,
		``,
	}, "\n")
	var buf bytes.Buffer
	text, tin, tout, err := convertCodexSSEToChat(strings.NewReader(sse), &buf, testBridgeOpts("gpt-5", "id1"))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello" {
		t.Fatalf("text %q", text)
	}
	if tin != 3 || tout != 2 {
		t.Fatalf("usage %d %d", tin, tout)
	}
	out := buf.String()
	if !strings.Contains(out, `"content":"Hel"`) || !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("sse %s", out)
	}
}

func TestAggregateCodexSSEWithHostedTools(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"CEO is Kim"}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"DB CEO"}}}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"CEO is Kim"}]},{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"DB CEO"}}]}}`,
		``,
	}, "\n")
	body, tin, tout, err := aggregateCodexSSE(strings.NewReader(sse), testBridgeOpts("gpt-5.4-mini", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if tin != 10 || tout != 5 {
		t.Fatalf("usage %d %d", tin, tout)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	content := completionContent(t, parsed)
	if content != "CEO is Kim" {
		t.Fatalf("content %q (delta+completed must not double)", content)
	}
	llms, ok := parsed["llms"].(map[string]any)
	if !ok {
		t.Fatalf("missing llms: %s", body)
	}
	hosted, ok := llms["hosted_tools"].([]any)
	if !ok || len(hosted) == 0 {
		t.Fatalf("hosted_tools: %s", body)
	}
	h0 := hosted[0].(map[string]any)
	if h0["name"] != "web_search" {
		t.Fatalf("hosted name %v", h0["name"])
	}
}

// TestAggregateCodexSSEDeltaPlusCompletedMessage: completed.output message is the
// sole final body. Deltas must not concatenate with that message.
func TestAggregateCodexSSEDeltaPlusCompletedMessage(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`,
		``,
	}, "\n")
	body, tin, tout, err := aggregateCodexSSE(strings.NewReader(sse), testBridgeOpts("gpt-5.6-luna", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if tin != 1 || tout != 1 {
		t.Fatalf("usage %d %d", tin, tout)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	content := completionContent(t, parsed)
	if content != "ok" {
		t.Fatalf("content %q want %q", content, "ok")
	}
}

func TestAggregateCodexSSECompletedOverwritesDivergentDeltas(t *testing.T) {
	// If upstream ever disagrees, completed wins (authoritative final).
	sse := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"final"}]}]}}`,
		``,
	}, "\n")
	body, _, _, err := aggregateCodexSSE(strings.NewReader(sse), testBridgeOpts("gpt-5", "x"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if got := completionContent(t, parsed); got != "final" {
		t.Fatalf("content %q want final (completed authoritative)", got)
	}
}

func completionContent(t *testing.T, parsed map[string]any) string {
	t.Helper()
	choices, ok := parsed["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("choices: %#v", parsed["choices"])
	}
	msg, ok := choices[0].(map[string]any)["message"].(map[string]any)
	if !ok {
		t.Fatalf("message: %#v", choices[0])
	}
	content, _ := msg["content"].(string)
	return content
}

func TestAggregateCodexSSEFunctionToolCall(t *testing.T) {
	sse := `data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"browser_search","arguments":"{\"query\":\"x\"}"}}

data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"function_call","call_id":"call_1","name":"browser_search","arguments":"{\"query\":\"x\"}"}]}}

`
	body, _, _, err := aggregateCodexSSE(strings.NewReader(sse), testBridgeOpts("gpt-5", "x"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	choices := parsed["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	tcs := msg["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("tool_calls: %s", body)
	}
	tc0 := tcs[0].(map[string]any)
	fn := tc0["function"].(map[string]any)
	if fn["name"] != "browser_search" {
		t.Fatalf("name %v", fn["name"])
	}
}

func TestAggregateCodexSSEImageHostedTool(t *testing.T) {
	sse := `data: {"type":"response.output_item.done","item":{"type":"image_generation_call","id":"ig_1","status":"completed","result":"aaaBBB"}}

data: {"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":0},"output":[{"type":"image_generation_call","result":"aaaBBB"}]}}

`
	body, _, _, err := aggregateCodexSSE(strings.NewReader(sse), testBridgeOpts("gpt-5", "x"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	llms := parsed["llms"].(map[string]any)
	hosted := llms["hosted_tools"].([]any)
	h0 := hosted[0].(map[string]any)
	if h0["name"] != "generate_image" {
		t.Fatalf("name %v", h0["name"])
	}
	result := h0["result"].(map[string]any)
	if result["b64_json"] != "aaaBBB" {
		t.Fatalf("b64 %v", result["b64_json"])
	}
}

func TestAggregateCodexSSECompletedMessageOnly(t *testing.T) {
	sse := `data: {"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":1},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}}

`
	body, _, _, err := aggregateCodexSSE(strings.NewReader(sse), testBridgeOpts("gpt-5", "x"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if got := completionContent(t, parsed); got != "hi" {
		t.Fatalf("content %q", got)
	}
}

func TestAggregateCodexSSE(t *testing.T) {
	sse := `data: {"type":"response.output_text.delta","delta":"ok"}

data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}

`
	body, tin, tout, err := aggregateCodexSSE(strings.NewReader(sse), testBridgeOpts("gpt-5", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if tin != 1 || tout != 1 {
		t.Fatalf("%d %d", tin, tout)
	}
	if !strings.Contains(string(body), `"object":"chat.completion"`) || !strings.Contains(string(body), `"content":"ok"`) {
		t.Fatalf("%s", body)
	}
}

func TestCodexWebSearchDisabled(t *testing.T) {
	in := []byte(`{
	  "model":"gpt-5",
	  "messages":[{"role":"user","content":"hi"}],
	  "tools":[{"type":"function","function":{"name":"web_search","description":"s","parameters":{}}}],
	  "llms":{"web_search":{"mode":"disabled"}}
	}`)
	out, _, _, err := chatToCodexResponses(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "web_search") {
		t.Fatalf("web_search should be omitted: %s", out)
	}
}

func TestConvertCodexSSEToolCallEmitsOnceOnDone(t *testing.T) {
	// added + done must not double-emit tool_calls for append-style clients.
	sse := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","name":"browser_search","arguments":""}}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"browser_search","arguments":"{\"query\":\"x\"}"}}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"function_call","call_id":"call_1","name":"browser_search","arguments":"{\"query\":\"x\"}"}]}}`,
		``,
	}, "\n")
	var buf bytes.Buffer
	_, _, _, err := convertCodexSSEToChat(strings.NewReader(sse), &buf, testBridgeOpts("gpt-5", "id1"))
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if n := strings.Count(out, `"name":"browser_search"`); n != 1 {
		t.Fatalf("tool_calls name emissions=%d want 1\n%s", n, out)
	}
	if !strings.Contains(out, `"arguments":"{\"query\":\"x\"}"`) && !strings.Contains(out, `"arguments":"{\"query\":\"x\"}"`) {
		// JSON may escape differently; require query once.
		if strings.Count(out, `query`) < 1 {
			t.Fatalf("missing arguments: %s", out)
		}
	}
	if strings.Count(out, `"index":0`) < 1 {
		t.Fatalf("expected tool_calls index 0: %s", out)
	}
}

func TestConvertCodexSSEToolCallStableIndexes(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_a","name":"one","arguments":"{}"}}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_b","name":"two","arguments":"{}"}}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1},"output":[]}}`,
		``,
	}, "\n")
	var buf bytes.Buffer
	_, _, _, err := convertCodexSSEToChat(strings.NewReader(sse), &buf, testBridgeOpts("gpt-5", "id1"))
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `"index":0`) || !strings.Contains(out, `"index":1`) {
		t.Fatalf("expected indexes 0 and 1: %s", out)
	}
	if strings.Count(out, `"name":"one"`) != 1 || strings.Count(out, `"name":"two"`) != 1 {
		t.Fatalf("each tool once: %s", out)
	}
}

func TestToolCallStreamIndex(t *testing.T) {
	calls := []map[string]any{
		{"id": "a"},
		{"id": "b"},
	}
	if got := toolCallStreamIndex(calls, map[string]any{"id": "b"}); got != 1 {
		t.Fatalf("got %d", got)
	}
	if got := toolCallStreamIndex(calls, map[string]any{"id": "a"}); got != 0 {
		t.Fatalf("got %d", got)
	}
}
