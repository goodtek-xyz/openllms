package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const anthropicDefaultMaxTokens = 4096

// chatToAnthropicMessages converts OpenAI chat.completions body to Anthropic Messages API body.
func chatToAnthropicMessages(chatBody []byte) (messagesBody []byte, clientStream bool, model string, err error) {
	var in struct {
		Model       string          `json:"model"`
		Stream      bool            `json:"stream"`
		MaxTokens   *int            `json:"max_tokens"`
		Temperature *float64        `json:"temperature"`
		Tools       json.RawMessage `json:"tools"`
		ToolChoice  json.RawMessage `json:"tool_choice"`
		LLMS        json.RawMessage `json:"llms"`
		Messages    []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCalls  json.RawMessage `json:"tool_calls"`
			ToolCallID string          `json:"tool_call_id"`
			Name       string          `json:"name"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(chatBody, &in); err != nil {
		return nil, false, "", err
	}
	model = strings.TrimSpace(in.Model)
	if model == "" {
		model = "claude-sonnet-4-5"
	}
	maxTokens := anthropicDefaultMaxTokens
	if in.MaxTokens != nil && *in.MaxTokens > 0 {
		maxTokens = *in.MaxTokens
	}

	var system strings.Builder
	var messages []map[string]any
	for _, m := range in.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		switch role {
		case "system", "developer":
			text := flattenChatContent(m.Content)
			if text == "" {
				continue
			}
			if system.Len() > 0 {
				system.WriteByte('\n')
			}
			system.WriteString(text)
		case "tool":
			block := map[string]any{
				"type":        "tool_result",
				"tool_use_id": strings.TrimSpace(m.ToolCallID),
				"content":     flattenChatContent(m.Content),
			}
			messages = appendAnthropicMessage(messages, "user", []map[string]any{block})
		case "assistant":
			var blocks []map[string]any
			blocks = append(blocks, chatContentToAnthropicBlocks(m.Content)...)
			// Anthropic assistant turns should not carry client image_url parts.
			blocks = filterAnthropicTextAndToolBlocks(blocks)
			blocks = append(blocks, anthropicToolUseBlocks(m.ToolCalls)...)
			if len(blocks) == 0 {
				continue
			}
			messages = appendAnthropicMessage(messages, "assistant", blocks)
		default:
			blocks := chatContentToAnthropicBlocks(m.Content)
			if len(blocks) == 0 {
				continue
			}
			messages = appendAnthropicMessage(messages, "user", blocks)
		}
	}
	if len(messages) == 0 {
		return nil, false, "", fmt.Errorf("anthropic bridge: no user/assistant messages")
	}

	out := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   messages,
	}
	if s := strings.TrimSpace(system.String()); s != "" {
		out["system"] = s
	}
	if in.Temperature != nil {
		out["temperature"] = *in.Temperature
	}
	if tools, tc, err := mapClientToolsToAnthropic(in.Tools, in.ToolChoice, in.LLMS); err != nil {
		return nil, false, "", err
	} else if len(tools) > 0 {
		out["tools"] = tools
		if tc != nil {
			out["tool_choice"] = tc
		}
	}
	if in.Stream {
		out["stream"] = true
	}
	b, err := json.Marshal(out)
	return b, in.Stream, model, err
}

func appendAnthropicMessage(messages []map[string]any, role string, blocks []map[string]any) []map[string]any {
	if len(blocks) == 0 {
		return messages
	}
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if lr, _ := last["role"].(string); lr == role {
			if content, ok := last["content"].([]map[string]any); ok {
				last["content"] = append(content, blocks...)
				messages[len(messages)-1] = last
				return messages
			}
		}
	}
	return append(messages, map[string]any{"role": role, "content": blocks})
}

func filterAnthropicTextAndToolBlocks(blocks []map[string]any) []map[string]any {
	var out []map[string]any
	for _, b := range blocks {
		typ, _ := b["type"].(string)
		if typ == "text" || typ == "tool_use" {
			out = append(out, b)
		}
	}
	return out
}

func anthropicToolUseBlocks(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var calls []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &calls) != nil {
		return nil
	}
	var out []map[string]any
	for _, c := range calls {
		name := strings.TrimSpace(c.Function.Name)
		if name == "" {
			continue
		}
		var input any = map[string]any{}
		if args := strings.TrimSpace(c.Function.Arguments); args != "" {
			var parsed any
			if json.Unmarshal([]byte(args), &parsed) == nil {
				input = parsed
			}
		}
		block := map[string]any{
			"type":  "tool_use",
			"id":    c.ID,
			"name":  name,
			"input": input,
		}
		out = append(out, block)
	}
	return out
}

type anthropicBridgeOpts struct {
	model     string
	id        string
	accountID string
	provider  string
}

func convertAnthropicMessageToChat(msg map[string]any, opts anthropicBridgeOpts) (body []byte, tin, tout int, err error) {
	if opts.id == "" {
		opts.id = "chatcmpl-claude"
	}
	state := newAnthropicParseState(opts, nil)
	state.ingestMessage(msg)
	return state.buildCompletionJSON(), state.tin, state.tout, nil
}

func convertAnthropicSSEToChat(r io.Reader, w io.Writer, opts anthropicBridgeOpts) (text string, tin, tout int, err error) {
	if opts.id == "" {
		opts.id = "chatcmpl-claude"
	}
	state := newAnthropicParseState(opts, w)
	if err := parseAnthropicSSE(r, state, w, opts); err != nil {
		return state.text.String(), state.tin, state.tout, err
	}
	if w != nil {
		if err := writeAnthropicStreamFinale(state, w, opts); err != nil {
			return state.text.String(), state.tin, state.tout, err
		}
	}
	return state.text.String(), state.tin, state.tout, nil
}

type pipeAnthropicToChat struct {
	pr   *io.PipeReader
	done chan struct{}
}

func startAnthropicChatPipe(upstream io.ReadCloser, opts anthropicBridgeOpts) *pipeAnthropicToChat {
	pr, pw := io.Pipe()
	p := &pipeAnthropicToChat{pr: pr, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		defer upstream.Close()
		defer pw.Close()
		_, _, _, err := convertAnthropicSSEToChat(upstream, pw, opts)
		if err != nil {
			_ = pw.CloseWithError(err)
		}
	}()
	return p
}

func (p *pipeAnthropicToChat) Read(b []byte) (int, error) { return p.pr.Read(b) }
func (p *pipeAnthropicToChat) Close() error {
	err := p.pr.Close()
	<-p.done
	return err
}

func anthropicFinishReason(stop string, hasToolCalls bool) string {
	switch stop {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	default:
		if hasToolCalls {
			return "tool_calls"
		}
		return "stop"
	}
}
