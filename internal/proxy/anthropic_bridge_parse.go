package proxy

import (
	"encoding/json"
	"io"
	"strings"
	"time"
)

type anthropicParseState struct {
	text           strings.Builder
	tin, tout      int
	hostedTools    []map[string]any
	toolCalls      []map[string]any
	pendingSearch  map[string]map[string]any
	blockStarts    map[int]map[string]any
	blockInputJSON map[int]string
	blockTypes     map[int]string
	stopReason     string
	accountID      string
	provider       string
	streamWriter   ioWriter
	model          string
	id             string
	contentEmitted bool
}

func newAnthropicParseState(opts anthropicBridgeOpts, w io.Writer) *anthropicParseState {
	st := &anthropicParseState{
		accountID:      opts.accountID,
		provider:       opts.provider,
		model:          opts.model,
		id:             opts.id,
		pendingSearch:  map[string]map[string]any{},
		blockStarts:    map[int]map[string]any{},
		blockInputJSON: map[int]string{},
		blockTypes:     map[int]string{},
	}
	if w != nil {
		st.streamWriter = &sseWriter{w: w, model: opts.model, id: opts.id}
	}
	return st
}

func (st *anthropicParseState) llmsMeta() map[string]any {
	meta := map[string]any{}
	if st.accountID != "" {
		meta["account_id"] = st.accountID
	}
	if st.provider != "" {
		meta["provider"] = st.provider
	}
	if len(st.hostedTools) > 0 {
		meta["hosted_tools"] = st.hostedTools
	}
	return meta
}

func (st *anthropicParseState) ingestMessage(msg map[string]any) {
	if u, ok := msg["usage"].(map[string]any); ok {
		st.tin = anyInt(u["input_tokens"])
		st.tout = anyInt(u["output_tokens"])
	}
	if sr, _ := msg["stop_reason"].(string); sr != "" {
		st.stopReason = sr
	}
	content, _ := msg["content"].([]any)
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		st.ingestContentBlock(block, false)
	}
}

func (st *anthropicParseState) ingestContentBlock(block map[string]any, streaming bool) {
	typ, _ := block["type"].(string)
	switch typ {
	case "text":
		if t, _ := block["text"].(string); t != "" {
			st.text.WriteString(t)
		}
	case "tool_use":
		if tc, ok := anthropicToolCallFromBlock(block); ok {
			st.toolCalls = mergeToolCall(st.toolCalls, tc)
		}
	case "server_tool_use":
		if ht, ok := anthropicHostedToolFromServerUse(block); ok {
			if id, _ := ht["id"].(string); id != "" {
				st.pendingSearch[id] = ht
			}
			st.hostedTools = mergeHostedTool(st.hostedTools, ht)
			if streaming && st.streamWriter != nil {
				_ = st.streamWriter.WriteHostedToolEvent("started", ht)
			}
		}
	case "web_search_tool_result":
		if ht, ok := anthropicHostedToolFromSearchResult(block, st.pendingSearch); ok {
			st.hostedTools = mergeHostedTool(st.hostedTools, ht)
			if streaming && st.streamWriter != nil {
				_ = st.streamWriter.WriteHostedToolEvent("completed", ht)
			}
		}
	}
}

func (st *anthropicParseState) buildCompletionJSON() []byte {
	text := st.text.String()
	finish := anthropicFinishReason(st.stopReason, len(st.toolCalls) > 0)
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
	if meta := st.llmsMeta(); len(meta) > 0 {
		out["llms"] = meta
	}
	b, _ := json.Marshal(out)
	return b
}

func parseAnthropicSSE(r io.Reader, state *anthropicParseState, w io.Writer, opts anthropicBridgeOpts) error {
	streaming := w != nil
	return forEachAnthropicSSEEvent(r, func(typ string, obj map[string]any) error {
		switch typ {
		case "message_start":
			if msg, ok := obj["message"].(map[string]any); ok {
				if u, ok := msg["usage"].(map[string]any); ok {
					state.tin = anyInt(u["input_tokens"])
				}
			}
		case "content_block_start":
			idx := anyInt(obj["index"])
			block, _ := obj["content_block"].(map[string]any)
			if block == nil {
				return nil
			}
			btyp, _ := block["type"].(string)
			state.blockTypes[idx] = btyp
			start := map[string]any{}
			for k, v := range block {
				start[k] = v
			}
			state.blockStarts[idx] = start
			switch btyp {
			case "server_tool_use", "web_search_tool_result":
				state.ingestContentBlock(block, streaming)
			}
		case "content_block_delta":
			idx := anyInt(obj["index"])
			delta, _ := obj["delta"].(map[string]any)
			if delta == nil {
				return nil
			}
			switch dtyp, _ := delta["type"].(string); dtyp {
			case "text_delta":
				t, _ := delta["text"].(string)
				if t == "" {
					return nil
				}
				state.text.WriteString(t)
				if streaming {
					state.contentEmitted = true
					return state.streamWriter.WriteChunk(map[string]any{
						"id": opts.id, "object": "chat.completion.chunk",
						"created": time.Now().Unix(), "model": opts.model,
						"choices": []map[string]any{{
							"index": 0, "delta": map[string]any{"content": t},
						}},
					})
				}
			case "input_json_delta":
				partial, _ := delta["partial_json"].(string)
				state.blockInputJSON[idx] = mergePartialJSON(state.blockInputJSON[idx], partial)
			}
		case "content_block_stop":
			idx := anyInt(obj["index"])
			btyp := state.blockTypes[idx]
			if btyp == "tool_use" {
				block := state.blockStarts[idx]
				if block == nil {
					block = map[string]any{"type": "tool_use"}
				}
				if raw := strings.TrimSpace(state.blockInputJSON[idx]); raw != "" {
					var input any
					if json.Unmarshal([]byte(raw), &input) == nil {
						block["input"] = input
					}
				}
				if tc, ok := anthropicToolCallFromBlock(block); ok {
					state.toolCalls = mergeToolCall(state.toolCalls, tc)
					if streaming {
						idx := toolCallStreamIndex(state.toolCalls, tc)
						return state.streamWriter.WriteChunk(map[string]any{
							"id": opts.id, "object": "chat.completion.chunk",
							"created": time.Now().Unix(), "model": opts.model,
							"choices": []map[string]any{{
								"index": 0,
								"delta": map[string]any{"tool_calls": []map[string]any{{
									"index": idx, "id": tc["id"], "type": "function",
									"function": tc["function"],
								}}},
							}},
						})
					}
				}
			}
			if btyp == "server_tool_use" {
				if block := state.blockStarts[idx]; block != nil {
					if action := searchActionFromInput(state.blockInputJSON[idx]); action != nil {
						if id, _ := block["id"].(string); id != "" {
							if ht, ok := state.pendingSearch[id]; ok {
								ht["action"] = action
								state.pendingSearch[id] = ht
								state.hostedTools = mergeHostedTool(state.hostedTools, ht)
							}
						}
					}
				}
			}
			delete(state.blockStarts, idx)
			delete(state.blockInputJSON, idx)
			delete(state.blockTypes, idx)
		case "message_delta":
			if delta, ok := obj["delta"].(map[string]any); ok {
				if sr, _ := delta["stop_reason"].(string); sr != "" {
					state.stopReason = sr
				}
			}
			if u, ok := obj["usage"].(map[string]any); ok {
				state.tout = anyInt(u["output_tokens"])
			}
		case "message_stop":
			// finale handled separately
		case "error":
			if errObj, ok := obj["error"].(map[string]any); ok {
				if msg, _ := errObj["message"].(string); msg != "" {
					return io.ErrUnexpectedEOF
				}
			}
		}
		return nil
	})
}

func writeAnthropicStreamFinale(state *anthropicParseState, w io.Writer, opts anthropicBridgeOpts) error {
	if !state.contentEmitted {
		if final := state.text.String(); final != "" {
			state.contentEmitted = true
			if err := state.streamWriter.WriteChunk(map[string]any{
				"id": opts.id, "object": "chat.completion.chunk",
				"created": time.Now().Unix(), "model": opts.model,
				"choices": []map[string]any{{
					"index": 0, "delta": map[string]any{"content": final},
				}},
			}); err != nil {
				return err
			}
		}
	}
	finish := anthropicFinishReason(state.stopReason, len(state.toolCalls) > 0)
	fin := map[string]any{
		"id": opts.id, "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": opts.model,
		"choices": []map[string]any{{
			"index": 0, "delta": map[string]any{}, "finish_reason": finish,
		}},
	}
	if state.tin+state.tout > 0 {
		fin["usage"] = map[string]any{
			"prompt_tokens": state.tin, "completion_tokens": state.tout,
			"total_tokens": state.tin + state.tout,
		}
	}
	if meta := state.llmsMeta(); len(meta) > 0 {
		fin["llms"] = meta
	}
	if err := state.streamWriter.WriteChunk(fin); err != nil {
		return err
	}
	_, err := io.WriteString(w, "data: [DONE]\n\n")
	return err
}
