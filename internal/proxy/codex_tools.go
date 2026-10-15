package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// mapClientToolsToCodex converts OpenAI function tools from clients into Codex Responses tools.
func mapClientToolsToCodex(toolsRaw json.RawMessage, toolChoiceRaw json.RawMessage, llmsRaw json.RawMessage) (upstreamTools []any, upstreamToolChoice any, err error) {
	if len(toolsRaw) == 0 || string(toolsRaw) == "null" {
		return nil, nil, nil
	}
	var tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(toolsRaw, &tools); err != nil {
		return nil, nil, err
	}
	webSearchLive := codexWebSearchLive(llmsRaw)
	var out []any
	hasWebSearch := false
	hasImageGen := false
	for _, t := range tools {
		if t.Type != "" && t.Type != "function" {
			continue
		}
		name := strings.TrimSpace(t.Function.Name)
		if name == "" {
			continue
		}
		switch name {
		case "web_search":
			if webSearchLive == nil {
				continue // disabled
			}
			out = append(out, codexWebSearchTool(*webSearchLive))
			hasWebSearch = true
		case "generate_image":
			out = append(out, codexImageGenerationTool())
			hasImageGen = true
		default:
			out = append(out, codexFunctionTool(name, t.Function.Description, t.Function.Parameters))
		}
	}
	if len(out) == 0 {
		return nil, nil, nil
	}
	tc := mapToolChoice(toolChoiceRaw, hasWebSearch, hasImageGen)
	return out, tc, nil
}

func codexWebSearchTool(externalLive bool) map[string]any {
	t := map[string]any{"type": "web_search"}
	if externalLive {
		t["external_web_access"] = true
	} else {
		t["external_web_access"] = false
	}
	return t
}

func codexImageGenerationTool() map[string]any {
	return codexImageGenerationToolOpts("auto", "auto", "auto")
}

func codexImageGenerationToolOpts(size, quality, background string) map[string]any {
	if size == "" {
		size = "auto"
	}
	if quality == "" {
		quality = "auto"
	}
	if background == "" {
		background = "auto"
	}
	return map[string]any{
		"type":          "image_generation",
		"output_format": "png",
		"size":          size,
		"quality":       quality,
		"background":    background,
	}
}

func codexFunctionTool(name, description string, params json.RawMessage) map[string]any {
	t := map[string]any{
		"type":   "function",
		"name":   name,
		"strict": false,
	}
	if description != "" {
		t["description"] = description
	}
	if len(params) > 0 && string(params) != "null" {
		var p any
		if json.Unmarshal(params, &p) == nil {
			t["parameters"] = p
		}
	} else {
		t["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return t
}

func mapToolChoice(raw json.RawMessage, hasWebSearch, hasImageGen bool) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch strings.ToLower(s) {
		case "none":
			return "none"
		case "required":
			if hasImageGen && !hasWebSearch {
				return map[string]any{"type": "image_generation"}
			}
			if hasWebSearch && !hasImageGen {
				return map[string]any{"type": "web_search"}
			}
			return "required"
		case "auto", "":
			return nil
		}
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		if fn, ok := obj["function"].(map[string]any); ok {
			if name, _ := fn["name"].(string); name != "" {
				switch name {
				case "web_search":
					return map[string]any{"type": "web_search"}
				case "generate_image":
					return map[string]any{"type": "image_generation"}
				default:
					return map[string]any{"type": "function", "name": name}
				}
			}
		}
		if typ, _ := obj["type"].(string); typ != "" {
			return obj
		}
	}
	return nil
}

// codexHostedToolFromItem maps a Responses output item to client llms.hosted_tools entry.
func codexHostedToolFromItem(item map[string]any) (map[string]any, bool) {
	typ, _ := item["type"].(string)
	switch typ {
	case "web_search_call":
		entry := map[string]any{"name": "web_search"}
		if id, _ := item["id"].(string); id != "" {
			entry["id"] = id
		}
		if st, _ := item["status"].(string); st != "" {
			entry["status"] = st
		}
		if action, ok := item["action"].(map[string]any); ok && len(action) > 0 {
			entry["action"] = action
		}
		return entry, true
	case "image_generation_call":
		entry := map[string]any{"name": "generate_image"}
		if id, _ := item["id"].(string); id != "" {
			entry["id"] = id
		}
		if st, _ := item["status"].(string); st != "" {
			entry["status"] = st
		} else {
			entry["status"] = "completed"
		}
		if b64 := imageGenerationB64(item); b64 != "" {
			entry["result"] = map[string]any{
				"b64_json":  b64,
				"mime_type": "image/png",
			}
		}
		return entry, true
	default:
		return nil, false
	}
}

// codexToolCallFromItem maps function_call output item to OpenAI tool_calls entry.
func codexToolCallFromItem(item map[string]any) (map[string]any, bool) {
	typ, _ := item["type"].(string)
	if typ != "function_call" {
		return nil, false
	}
	id, _ := item["call_id"].(string)
	if id == "" {
		id, _ = item["id"].(string)
	}
	name, _ := item["name"].(string)
	args, _ := item["arguments"].(string)
	if name == "" {
		return nil, false
	}
	return map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}, true
}

func mergeHostedTool(existing []map[string]any, entry map[string]any) []map[string]any {
	id, _ := entry["id"].(string)
	name, _ := entry["name"].(string)
	for i, h := range existing {
		hid, _ := h["id"].(string)
		hname, _ := h["name"].(string)
		if (id != "" && hid == id) || (id == "" && name != "" && hname == name) {
			merged := map[string]any{}
			for k, v := range existing[i] {
				merged[k] = v
			}
			for k, v := range entry {
				merged[k] = v
			}
			existing[i] = merged
			return existing
		}
	}
	return append(existing, entry)
}

func mergeToolCall(existing []map[string]any, tc map[string]any) []map[string]any {
	id, _ := tc["id"].(string)
	for i, e := range existing {
		eid, _ := e["id"].(string)
		if id != "" && eid == id {
			existing[i] = tc
			return existing
		}
	}
	return append(existing, tc)
}

// toolCallStreamIndex is the OpenAI chat-completions tool_calls[].index for a
// streamed delta (position in the merged toolCalls slice, not hard-coded 0).
func toolCallStreamIndex(calls []map[string]any, tc map[string]any) int {
	id, _ := tc["id"].(string)
	for i, e := range calls {
		eid, _ := e["id"].(string)
		if id != "" && eid == id {
			return i
		}
	}
	if n := len(calls); n > 0 {
		return n - 1
	}
	return 0
}

func parseCompletedOutput(resp map[string]any, state *codexParseState) {
	out, _ := resp["output"].([]any)
	for _, item := range out {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// Tools / tool_calls only — never assistant message text here.
		state.ingestOutputItem(m, false)
	}
	// completed.output message text is the authoritative final body. Deltas
	// are for streaming to the client; replacing avoids a delta+message
	// double-append ("ok" + "ok" → "okok").
	if final := extractCompletedText(resp); final != "" {
		state.text.Reset()
		state.text.WriteString(final)
	}
}

type codexParseState struct {
	text           strings.Builder
	tin, tout      int
	hostedTools    []map[string]any
	toolCalls      []map[string]any
	accountID      string
	provider       string
	streamWriter   ioWriter
	model          string
	id             string
	contentEmitted bool // true once a content delta was written to the client
}

type ioWriter interface {
	WriteChunk(chunk map[string]any) error
	WriteHostedToolEvent(phase string, tool map[string]any) error
}

type sseWriter struct {
	w     io.Writer
	model string
	id    string
}

func (s *sseWriter) WriteChunk(chunk map[string]any) error {
	b, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(s.w, "data: %s\n\n", b)
	return err
}

func (s *sseWriter) WriteHostedToolEvent(phase string, tool map[string]any) error {
	hosted := map[string]any{
		"phase": phase,
		"name":  tool["name"],
	}
	if id, ok := tool["id"]; ok {
		hosted["id"] = id
	}
	if st, ok := tool["status"]; ok {
		hosted["status"] = st
	}
	if action, ok := tool["action"]; ok {
		hosted["action"] = action
	}
	if result, ok := tool["result"]; ok {
		hosted["result"] = result
	}
	return s.WriteChunk(map[string]any{
		"id": s.id, "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": s.model,
		"llms": map[string]any{"hosted_tool": hosted},
	})
}

func (st *codexParseState) ingestOutputItem(item map[string]any, streaming bool) {
	if ht, ok := codexHostedToolFromItem(item); ok {
		st.hostedTools = mergeHostedTool(st.hostedTools, ht)
		if streaming && st.streamWriter != nil {
			phase := "started"
			if status, _ := item["status"].(string); status == "completed" {
				phase = "completed"
			}
			_ = st.streamWriter.WriteHostedToolEvent(phase, ht)
		}
	}
	if tc, ok := codexToolCallFromItem(item); ok {
		st.toolCalls = mergeToolCall(st.toolCalls, tc)
	}
	// Do not append type=message text. Text ownership:
	//   - streaming: response.output_text.delta → client chunks + state.text
	//   - final:     parseCompletedOutput → extractCompletedText (replace)
	// Message items in output_item.* / completed.output are tool-scan only.
}

func (st *codexParseState) buildCompletionJSON() []byte {
	text := st.text.String()
	finish := "stop"
	if len(st.toolCalls) > 0 {
		finish = "tool_calls"
	}
	msg := map[string]any{"role": "assistant", "content": text}
	if len(st.toolCalls) > 0 {
		msg["tool_calls"] = st.toolCalls
		if text == "" {
			msg["content"] = nil
		}
	}
	out := map[string]any{
		"id":      st.id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   st.model,
		"choices": []map[string]any{{
			"index": 0, "message": msg, "finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens": st.tin, "completion_tokens": st.tout, "total_tokens": st.tin + st.tout,
		},
	}
	llmsMeta := st.llmsMeta()
	if len(llmsMeta) > 0 {
		out["llms"] = llmsMeta
	}
	b, _ := json.Marshal(out)
	return b
}
