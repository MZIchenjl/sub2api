package devin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/google/uuid"
)

const interruptedToolResultText = "Tool use was interrupted and did not produce a result."

// TranslateOptions controls Anthropic → Devin translation behavior.
type TranslateOptions struct {
	// SessionID / CascadeID stabilize prompt caching across turns.
	SessionID string
	CascadeID string
	// StripSignatures omits thinking signatures on retry when Devin rejects cross-provider signatures.
	StripSignatures bool
	// RequestedEffort overrides effort parsed from the Anthropic body when non-empty.
	RequestedEffort string
}

// AnthropicRequestToDevin translates an Anthropic Messages request into Devin GetChatMessage parameters.
// System blocks and message content blocks preserve cache_control breakpoints so Devin's prompt cache hits.
func AnthropicRequestToDevin(req *apicompat.AnthropicRequest, targetModel string, opts TranslateOptions) (*ChatRequestParams, error) {
	if req == nil {
		return nil, fmt.Errorf("anthropic request is nil")
	}

	// Determine requested thinking effort / budget for model UID resolution.
	effort := strings.TrimSpace(opts.RequestedEffort)
	if effort == "" && req.OutputConfig != nil {
		effort = strings.TrimSpace(req.OutputConfig.Effort)
	}
	budgetTokens := 0
	if req.Thinking != nil {
		switch strings.ToLower(strings.TrimSpace(req.Thinking.Type)) {
		case "disabled":
			if effort == "" {
				effort = "none"
			}
		case "enabled", "adaptive":
			budgetTokens = req.Thinking.BudgetTokens
			if effort == "" && budgetTokens == 0 {
				effort = "high"
			}
		}
	}

	modelToResolve := strings.TrimSpace(targetModel)
	if modelToResolve == "" {
		modelToResolve = strings.TrimSpace(req.Model)
	}
	chatModelUID := ResolveChatModelUID(modelToResolve, effort, budgetTokens)

	// Parse tools (unless tool_choice == none).
	toolsDisabled := isAnthropicToolChoiceNone(req.ToolChoice)
	var tools []Tool
	offeredTools := make(map[string]struct{})
	if !toolsDisabled {
		for _, t := range req.Tools {
			name := strings.TrimSpace(t.Name)
			if name == "" {
				continue
			}
			// Skip server-side Anthropic built-in tool types without schemas (e.g. web_search_20250305).
			if t.Type != "" && t.Type != "custom" && t.Type != "function" {
				continue
			}
			schema := normalizeToolSchema(t.InputSchema)
			tools = append(tools, Tool{
				Name:        name,
				Description: t.Description,
				Parameters:  schema,
			})
			offeredTools[name] = struct{}{}
		}
	}

	// Extract system prompt (preserving cache_control blocks as leading prompts when marked).
	systemPrompt, systemCachedPrompts := extractSystemPromptAndBlocks(req.System)

	// Translate messages into Devin Prompts.
	var prompts []Prompt
	prompts = append(prompts, systemCachedPrompts...)

	var pendingToolCalls []ToolCall
	answerPendingCalls := func() {
		for _, tc := range pendingToolCalls {
			prompts = append(prompts, Prompt{
				Source:     PromptSourceTool,
				ToolCallID: tc.ID,
				Content:    interruptedToolResultText,
			})
		}
		pendingToolCalls = nil
	}

	for _, msg := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		blocks := parseAnthropicContentBlocks(msg.Content)

		switch role {
		case "assistant":
			answerPendingCalls()
			var textParts []string
			var thinkingParts []string
			var signature []byte
			var calls []ToolCall
			hasCache := false

			for _, b := range blocks {
				if b.CacheControl != nil {
					hasCache = true
				}
				switch b.Type {
				case "text":
					if b.Text != "" {
						textParts = append(textParts, b.Text)
					}
				case "thinking":
					if b.Thinking != "" {
						thinkingParts = append(thinkingParts, b.Thinking)
					}
					if !opts.StripSignatures && b.Signature != "" && len(signature) == 0 {
						signature = decodeAnthropicSignature(b.Signature)
					}
				case "redacted_thinking":
					if !opts.StripSignatures && b.Data != "" && len(signature) == 0 {
						signature = decodeAnthropicSignature(b.Data)
					}
				case "tool_use":
					origID := DecodeToolCallID(b.ID)
					args := strings.TrimSpace(string(b.Input))
					if args == "" || args == "null" {
						args = "{}"
					}
					calls = append(calls, ToolCall{
						ID:        origID,
						Name:      b.Name,
						Arguments: args,
					})
					if _, exists := offeredTools[b.Name]; !exists && b.Name != "" {
						offeredTools[b.Name] = struct{}{}
						tools = append(tools, Tool{
							Name:        b.Name,
							Description: b.Name,
							Parameters:  []byte(`{"type":"object","properties":{}}`),
						})
					}
				}
			}

			content := strings.Join(textParts, "\n")
			thinkingText := strings.Join(thinkingParts, "\n")
			if content == "" && len(calls) == 0 && thinkingText == "" {
				continue
			}
			sigType := ""
			if len(signature) > 0 {
				sigType = inferSignatureType(chatModelUID)
			}
			prompts = append(prompts, Prompt{
				Source:        PromptSourceAssistant,
				Content:       content,
				ToolCalls:     calls,
				CacheControl:  hasCache,
				Thinking:      thinkingText,
				Signature:     signature,
				SignatureType: sigType,
			})
			pendingToolCalls = append([]ToolCall(nil), calls...)

		case "user":
			// First process any tool_result blocks so they immediately follow assistant tool_calls.
			var userBlocks []apicompat.AnthropicContentBlock
			for _, b := range blocks {
				if b.Type == "tool_result" {
					origID := DecodeToolCallID(b.ToolUseID)
					text, imgs := extractToolResultContent(b.Content, b.IsError)
					if text == "" && len(imgs) == 0 {
						text = "(no output)"
					}
					matchedIdx := -1
					for idx, pending := range pendingToolCalls {
						if pending.ID == origID {
							matchedIdx = idx
							break
						}
					}
					if matchedIdx >= 0 {
						pendingToolCalls = append(pendingToolCalls[:matchedIdx], pendingToolCalls[matchedIdx+1:]...)
						prompts = append(prompts, Prompt{
							Source:       PromptSourceTool,
							ToolCallID:   origID,
							Content:      text,
							Images:       imgs,
							CacheControl: b.CacheControl != nil,
						})
					} else {
						// Orphaned tool result: downgrade to user text to avoid protocol rejection.
						userBlocks = append(userBlocks, apicompat.AnthropicContentBlock{
							Type:         "text",
							Text:         text,
							CacheControl: b.CacheControl,
						})
					}
					continue
				}
				userBlocks = append(userBlocks, b)
			}

			if len(userBlocks) > 0 {
				answerPendingCalls()
				// Keep blocks separated when any block carries cache_control so prompt cache breakpoints survive.
				anyCache := false
				for _, b := range userBlocks {
					if b.CacheControl != nil {
						anyCache = true
						break
					}
				}
				if anyCache {
					var pendingImgs []Image
					for _, b := range userBlocks {
						switch b.Type {
						case "image":
							if img, ok := anthropicBlockToImage(b); ok {
								pendingImgs = append(pendingImgs, img)
							}
						case "text":
							if strings.TrimSpace(b.Text) == "" && len(pendingImgs) == 0 {
								continue
							}
							prompts = append(prompts, Prompt{
								Source:       PromptSourceUser,
								Content:      b.Text,
								Images:       pendingImgs,
								CacheControl: b.CacheControl != nil,
							})
							pendingImgs = nil
						}
					}
					if len(pendingImgs) > 0 {
						prompts = append(prompts, Prompt{
							Source: PromptSourceUser,
							Images: pendingImgs,
						})
					}
				} else {
					var texts []string
					var imgs []Image
					for _, b := range userBlocks {
						switch b.Type {
						case "text":
							if strings.TrimSpace(b.Text) != "" {
								texts = append(texts, b.Text)
							}
						case "image":
							if img, ok := anthropicBlockToImage(b); ok {
								imgs = append(imgs, img)
							}
						}
					}
					joined := strings.Join(texts, "\n\n")
					if strings.TrimSpace(joined) != "" || len(imgs) > 0 {
						prompts = append(prompts, Prompt{
							Source:  PromptSourceUser,
							Content: joined,
							Images:  imgs,
						})
					}
				}
			}
		}
	}

	answerPendingCalls()

	if len(prompts) == 0 {
		prompts = append(prompts, Prompt{
			Source:  PromptSourceUser,
			Content: "Hello",
		})
	} else {
		last := prompts[len(prompts)-1]
		if last.Source == PromptSourceAssistant && len(last.ToolCalls) == 0 {
			prompts = append(prompts, Prompt{
				Source:  PromptSourceUser,
				Content: "Please continue.",
			})
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	cascadeID := strings.TrimSpace(opts.CascadeID)
	if cascadeID == "" {
		cascadeID = sessionID
	}

	return &ChatRequestParams{
		ModelUID:     chatModelUID,
		SystemPrompt: systemPrompt,
		Prompts:      prompts,
		Tools:        tools,
		Temperature:  req.Temperature,
		TopP:         req.TopP,
		MaxTokens:    maxTokens,
		SessionID:    sessionID,
		CascadeID:    cascadeID,
	}, nil
}

func isAnthropicToolChoiceNone(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var obj struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(trimmed, &obj); err == nil {
		return strings.EqualFold(strings.TrimSpace(obj.Type), "none")
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		return strings.EqualFold(strings.TrimSpace(s), "none")
	}
	return false
}

func normalizeToolSchema(raw json.RawMessage) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || !json.Valid(trimmed) {
		return []byte(`{"type":"object","properties":{}}`)
	}
	return trimmed
}

func extractSystemPromptAndBlocks(raw json.RawMessage) (string, []Prompt) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var str string
	if err := json.Unmarshal(trimmed, &str); err == nil {
		return str, nil
	}
	var blocks []apicompat.AnthropicContentBlock
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return "", nil
	}
	// If any system block carries cache_control, preserve it as user prompts? Wait:
	// Devin accepts Field 2 (system prompt) cleanly; keep non-empty sanitized text in Field 2.
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "" {
			if cleaned := SanitizeSystemPrompt(b.Text); cleaned != "" {
				parts = append(parts, cleaned)
			}
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func parseAnthropicContentBlocks(raw json.RawMessage) []apicompat.AnthropicContentBlock {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var str string
	if err := json.Unmarshal(trimmed, &str); err == nil {
		return []apicompat.AnthropicContentBlock{{Type: "text", Text: str}}
	}
	var blocks []apicompat.AnthropicContentBlock
	if err := json.Unmarshal(trimmed, &blocks); err == nil {
		return blocks
	}
	return nil
}

func extractToolResultContent(raw json.RawMessage, isError bool) (string, []Image) {
	blocks := parseAnthropicContentBlocks(raw)
	var texts []string
	var imgs []Image
	for _, b := range blocks {
		switch b.Type {
		case "text", "":
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		case "image":
			if img, ok := anthropicBlockToImage(b); ok {
				imgs = append(imgs, img)
			}
		}
	}
	joined := strings.Join(texts, "\n")
	if isError && joined != "" && !strings.HasPrefix(joined, "Error:") {
		joined = "Error: " + joined
	}
	return joined, imgs
}

func anthropicBlockToImage(b apicompat.AnthropicContentBlock) (Image, bool) {
	if b.Source == nil || strings.TrimSpace(b.Source.Data) == "" {
		return Image{}, false
	}
	return Image{
		Base64Data: b.Source.Data,
		MimeType:   DetectImageMIME(b.Source.Data, b.Source.MediaType),
	}, true
}

func decodeAnthropicSignature(sig string) []byte {
	trimmed := strings.TrimSpace(sig)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "sealed.v1.") {
		return []byte(trimmed)
	}
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(decoded) > 0 {
		return decoded
	}
	return []byte(trimmed)
}

func encodeSignatureForAnthropic(sig []byte) string {
	if len(sig) == 0 {
		return ""
	}
	if bytes.HasPrefix(sig, []byte("sealed.v1.")) || isPrintableASCII(sig) {
		return string(sig)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func inferSignatureType(modelUID string) string {
	lower := strings.ToLower(modelUID)
	switch {
	case strings.Contains(lower, "gemini"):
		return "gemini"
	case strings.Contains(lower, "claude"):
		return "anthropic"
	default:
		return ""
	}
}

// StreamProcessor converts sequential Devin Connect-RPC FrameResults into Anthropic SSE events
// and accumulates the full AnthropicResponse for non-streaming callers.
type StreamProcessor struct {
	model         string
	messageID     string
	started       bool
	finished      bool
	blockIndex    int
	activeBlock   string // "", "thinking", "text", "tool_use"
	hasToolCalls  bool
	hasMeaningful bool

	textUTF8     UTF8SplitBuffer
	thinkingUTF8 UTF8SplitBuffer

	// Accumulated non-streaming state
	currentText      strings.Builder
	currentThinking  strings.Builder
	currentSignature strings.Builder
	contentBlocks    []apicompat.AnthropicContentBlock
	toolCallsByID    map[string]*accumulatedToolCall
	toolCallOrder    []string

	// Track active tool call so switching to a second tool call closes the first block cleanly.
	activeToolKey string

	// Usage tracking
	promptTokens     int64
	completionTokens int64
	cachedTokens     int64
	cacheWriteTokens int64
	dimGroups        [][]byte
	upstreamModel    string
	upstreamReqID    string
	stopReason       string
}

type accumulatedToolCall struct {
	id         string
	encodedID  string
	name       string
	args       strings.Builder
	blockIndex int
	started    bool
	closed     bool
}

// NewStreamProcessor creates a StreamProcessor targeting the given client-visible model name.
func NewStreamProcessor(model string) *StreamProcessor {
	return &StreamProcessor{
		model:         model,
		messageID:     "msg_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:24],
		toolCallsByID: make(map[string]*accumulatedToolCall),
	}
}

// HasMeaningfulContent reports whether any text, thinking, or tool call was observed.
func (p *StreamProcessor) HasMeaningfulContent() bool {
	return p.hasMeaningful
}

// UpstreamModel returns the upstream model identifier reported in usage metadata, if any.
func (p *StreamProcessor) UpstreamModel() string {
	return p.upstreamModel
}

// UpstreamRequestID returns the upstream request ID reported in usage metadata, if any.
func (p *StreamProcessor) UpstreamRequestID() string {
	return p.upstreamReqID
}

// ProcessFrame consumes one decoded Devin response frame and returns the resulting Anthropic SSE events.
func (p *StreamProcessor) ProcessFrame(frame FrameResult) []apicompat.AnthropicStreamEvent {
	var events []apicompat.AnthropicStreamEvent

	if frame.MessageID != "" && !p.started {
		p.messageID = "msg_" + strings.TrimPrefix(frame.MessageID, "msg_")
	}

	if frame.Usage != nil {
		if frame.Usage.PromptTokens > p.promptTokens {
			p.promptTokens = frame.Usage.PromptTokens
		}
		if frame.Usage.CompletionTokens > p.completionTokens {
			p.completionTokens = frame.Usage.CompletionTokens
		}
		if frame.Usage.CachedTokens > p.cachedTokens {
			p.cachedTokens = frame.Usage.CachedTokens
		}
		if frame.Usage.CacheWriteTokens > p.cacheWriteTokens {
			p.cacheWriteTokens = frame.Usage.CacheWriteTokens
		}
		if frame.Usage.ModelName != "" {
			p.upstreamModel = frame.Usage.ModelName
		}
		if frame.Usage.RequestID != "" {
			p.upstreamReqID = frame.Usage.RequestID
		}
	}
	if len(frame.ResponseDimensionGroups) > 0 {
		p.dimGroups = append(p.dimGroups, frame.ResponseDimensionGroups...)
	}

	if frame.StopReason != 0 {
		switch frame.StopReason {
		case 3:
			p.stopReason = "max_tokens"
		case 10:
			p.stopReason = "tool_use"
		default:
			if p.stopReason == "" {
				p.stopReason = "end_turn"
			}
		}
	}

	ensureStarted := func() {
		if p.started {
			return
		}
		p.started = true
		events = append(events, apicompat.AnthropicStreamEvent{
			Type: "message_start",
			Message: &apicompat.AnthropicResponse{
				ID:      p.messageID,
				Type:    "message",
				Role:    "assistant",
				Content: []apicompat.AnthropicContentBlock{},
				Model:   p.model,
				Usage: apicompat.AnthropicUsage{
					InputTokens:              int(p.promptTokens),
					CacheCreationInputTokens: int(p.cacheWriteTokens),
					CacheReadInputTokens:     int(p.cachedTokens),
				},
			},
		})
	}

	closeToolCall := func(tc *accumulatedToolCall) {
		if tc == nil || tc.closed {
			return
		}
		if !tc.started {
			tc.started = true
			name := tc.name
			if name == "" {
				name = "unknown"
			}
			events = append(events, apicompat.AnthropicStreamEvent{
				Type:  "content_block_start",
				Index: intPtr(tc.blockIndex),
				ContentBlock: &apicompat.AnthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.encodedID,
					Name:  name,
					Input: json.RawMessage(`{}`),
				},
			})
			if tc.args.Len() > 0 {
				events = append(events, apicompat.AnthropicStreamEvent{
					Type:  "content_block_delta",
					Index: intPtr(tc.blockIndex),
					Delta: &apicompat.AnthropicDelta{
						Type:        "input_json_delta",
						PartialJSON: tc.args.String(),
					},
				})
			}
		}
		events = append(events, apicompat.AnthropicStreamEvent{
			Type:  "content_block_stop",
			Index: intPtr(tc.blockIndex),
		})
		tc.closed = true
		rawArgs := strings.TrimSpace(tc.args.String())
		if rawArgs == "" || !json.Valid([]byte(rawArgs)) {
			rawArgs = "{}"
		}
		p.contentBlocks = append(p.contentBlocks, apicompat.AnthropicContentBlock{
			Type:  "tool_use",
			ID:    tc.encodedID,
			Name:  tc.name,
			Input: json.RawMessage(rawArgs),
		})
	}

	closeActiveBlock := func() {
		if p.activeBlock == "" {
			return
		}
		idx := p.blockIndex
		switch p.activeBlock {
		case "thinking":
			p.contentBlocks = append(p.contentBlocks, apicompat.AnthropicContentBlock{
				Type:      "thinking",
				Thinking:  p.currentThinking.String(),
				Signature: p.currentSignature.String(),
			})
			p.currentThinking.Reset()
			p.currentSignature.Reset()
			events = append(events, apicompat.AnthropicStreamEvent{
				Type:  "content_block_stop",
				Index: intPtr(idx),
			})
		case "text":
			p.contentBlocks = append(p.contentBlocks, apicompat.AnthropicContentBlock{
				Type: "text",
				Text: p.currentText.String(),
			})
			p.currentText.Reset()
			events = append(events, apicompat.AnthropicStreamEvent{
				Type:  "content_block_stop",
				Index: intPtr(idx),
			})
		case "tool_use":
			if p.activeToolKey != "" {
				closeToolCall(p.toolCallsByID[p.activeToolKey])
				p.activeToolKey = ""
			}
		}
		p.blockIndex++
		p.activeBlock = ""
	}

	// 1. Thinking text / signature deltas
	thinkingChunk := ""
	if frame.ThinkingText != "" {
		thinkingChunk = p.thinkingUTF8.Feed([]byte(frame.ThinkingText))
	}
	sigChunk := encodeSignatureForAnthropic(frame.DeltaSignature)
	if thinkingChunk != "" || sigChunk != "" {
		p.hasMeaningful = true
		ensureStarted()
		if p.activeBlock != "thinking" {
			closeActiveBlock()
			p.activeBlock = "thinking"
			events = append(events, apicompat.AnthropicStreamEvent{
				Type:  "content_block_start",
				Index: intPtr(p.blockIndex),
				ContentBlock: &apicompat.AnthropicContentBlock{
					Type:     "thinking",
					Thinking: "",
				},
			})
		}
		if thinkingChunk != "" {
			p.currentThinking.WriteString(thinkingChunk)
			events = append(events, apicompat.AnthropicStreamEvent{
				Type:  "content_block_delta",
				Index: intPtr(p.blockIndex),
				Delta: &apicompat.AnthropicDelta{
					Type:     "thinking_delta",
					Thinking: thinkingChunk,
				},
			})
		}
		if sigChunk != "" {
			p.currentSignature.WriteString(sigChunk)
			events = append(events, apicompat.AnthropicStreamEvent{
				Type:  "content_block_delta",
				Index: intPtr(p.blockIndex),
				Delta: &apicompat.AnthropicDelta{
					Type:      "signature_delta",
					Signature: sigChunk,
				},
			})
		}
	}

	// 2. Text content deltas
	if frame.ContentText != "" {
		if textChunk := p.textUTF8.Feed([]byte(frame.ContentText)); textChunk != "" {
			p.hasMeaningful = true
			ensureStarted()
			if p.activeBlock != "text" {
				closeActiveBlock()
				p.activeBlock = "text"
				events = append(events, apicompat.AnthropicStreamEvent{
					Type:  "content_block_start",
					Index: intPtr(p.blockIndex),
					ContentBlock: &apicompat.AnthropicContentBlock{
						Type: "text",
						Text: "",
					},
				})
			}
			p.currentText.WriteString(textChunk)
			events = append(events, apicompat.AnthropicStreamEvent{
				Type:  "content_block_delta",
				Index: intPtr(p.blockIndex),
				Delta: &apicompat.AnthropicDelta{
					Type: "text_delta",
					Text: textChunk,
				},
			})
		}
	}

	// 3. Tool call deltas
	for _, delta := range frame.ToolCallDeltas {
		if delta.ID == "" && delta.Name == "" && delta.Arguments == "" {
			continue
		}
		p.hasMeaningful = true
		p.hasToolCalls = true
		ensureStarted()

		key := delta.ID
		if key == "" && len(p.toolCallOrder) > 0 {
			key = p.toolCallOrder[len(p.toolCallOrder)-1]
		}
		if key == "" {
			key = "call_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:16]
		}

		tc, exists := p.toolCallsByID[key]
		if !exists {
			closeActiveBlock()
			tc = &accumulatedToolCall{
				id:         key,
				encodedID:  EncodeToolCallID(key),
				name:       delta.Name,
				blockIndex: p.blockIndex,
			}
			p.toolCallsByID[key] = tc
			p.toolCallOrder = append(p.toolCallOrder, key)
			p.activeBlock = "tool_use"
			p.activeToolKey = key
		}
		if delta.Name != "" && tc.name == "" {
			tc.name = delta.Name
		}
		if !tc.started && tc.name != "" {
			tc.started = true
			events = append(events, apicompat.AnthropicStreamEvent{
				Type:  "content_block_start",
				Index: intPtr(tc.blockIndex),
				ContentBlock: &apicompat.AnthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.encodedID,
					Name:  tc.name,
					Input: json.RawMessage(`{}`),
				},
			})
		}
		if delta.Arguments != "" {
			tc.args.WriteString(delta.Arguments)
			if tc.started {
				events = append(events, apicompat.AnthropicStreamEvent{
					Type:  "content_block_delta",
					Index: intPtr(tc.blockIndex),
					Delta: &apicompat.AnthropicDelta{
						Type:        "input_json_delta",
						PartialJSON: delta.Arguments,
					},
				})
			}
		}
	}

	return events
}

// Finalize closes any open blocks, resolves final usage and stop_reason, and returns terminal SSE events.
func (p *StreamProcessor) Finalize() ([]apicompat.AnthropicStreamEvent, *apicompat.AnthropicResponse, Usage) {
	if p.finished {
		return nil, p.buildResponse(), p.buildUsage()
	}
	p.finished = true

	// Merge Field 28 dimension-group token usage if Field 7 was missing.
	if len(p.dimGroups) > 0 {
		if dimPrompt, dimComp, dimCached, ok := ParseResponseDimensionGroups(p.dimGroups...); ok {
			if p.promptTokens == 0 && dimPrompt > 0 {
				p.promptTokens = dimPrompt
			}
			if p.completionTokens == 0 && dimComp > 0 {
				p.completionTokens = dimComp
			}
			if p.cachedTokens == 0 && dimCached > 0 {
				p.cachedTokens = dimCached
			}
		}
	}

	var events []apicompat.AnthropicStreamEvent

	if !p.started {
		// Nothing meaningful emitted; leave started=false so caller can treat empty stream as 502 failover.
		return nil, p.buildResponse(), p.buildUsage()
	}

	// Close any open non-tool block.
	switch p.activeBlock {
	case "thinking":
		p.contentBlocks = append(p.contentBlocks, apicompat.AnthropicContentBlock{
			Type:      "thinking",
			Thinking:  p.currentThinking.String(),
			Signature: p.currentSignature.String(),
		})
		p.currentThinking.Reset()
		p.currentSignature.Reset()
		events = append(events, apicompat.AnthropicStreamEvent{
			Type:  "content_block_stop",
			Index: intPtr(p.blockIndex),
		})
		p.blockIndex++
		p.activeBlock = ""
	case "text":
		p.contentBlocks = append(p.contentBlocks, apicompat.AnthropicContentBlock{
			Type: "text",
			Text: p.currentText.String(),
		})
		p.currentText.Reset()
		events = append(events, apicompat.AnthropicStreamEvent{
			Type:  "content_block_stop",
			Index: intPtr(p.blockIndex),
		})
		p.blockIndex++
		p.activeBlock = ""
	case "tool_use":
		// Closed in loop below
		p.activeBlock = ""
	}

	// Finalize all tool calls in order.
	for _, key := range p.toolCallOrder {
		tc := p.toolCallsByID[key]
		if tc == nil || tc.closed {
			continue
		}
		if !tc.started {
			tc.started = true
			name := tc.name
			if name == "" {
				name = "unknown"
			}
			events = append(events, apicompat.AnthropicStreamEvent{
				Type:  "content_block_start",
				Index: intPtr(tc.blockIndex),
				ContentBlock: &apicompat.AnthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.encodedID,
					Name:  name,
					Input: json.RawMessage(`{}`),
				},
			})
			if tc.args.Len() > 0 {
				events = append(events, apicompat.AnthropicStreamEvent{
					Type:  "content_block_delta",
					Index: intPtr(tc.blockIndex),
					Delta: &apicompat.AnthropicDelta{
						Type:        "input_json_delta",
						PartialJSON: tc.args.String(),
					},
				})
			}
		}
		events = append(events, apicompat.AnthropicStreamEvent{
			Type:  "content_block_stop",
			Index: intPtr(tc.blockIndex),
		})
		tc.closed = true
		p.blockIndex++

		rawArgs := strings.TrimSpace(tc.args.String())
		if rawArgs == "" || !json.Valid([]byte(rawArgs)) {
			rawArgs = "{}"
		}
		p.contentBlocks = append(p.contentBlocks, apicompat.AnthropicContentBlock{
			Type:  "tool_use",
			ID:    tc.encodedID,
			Name:  tc.name,
			Input: json.RawMessage(rawArgs),
		})
	}

	stopReason := p.stopReason
	if p.hasToolCalls {
		stopReason = "tool_use"
	} else if stopReason == "" {
		stopReason = "end_turn"
	}
	p.stopReason = stopReason

	usage := p.buildUsage()
	events = append(events,
		apicompat.AnthropicStreamEvent{
			Type: "message_delta",
			Delta: &apicompat.AnthropicDelta{
				StopReason: stopReason,
			},
			Usage: &apicompat.AnthropicUsage{
				InputTokens:              int(usage.PromptTokens),
				OutputTokens:             int(usage.CompletionTokens),
				CacheCreationInputTokens: int(usage.CacheWriteTokens),
				CacheReadInputTokens:     int(usage.CachedTokens),
			},
		},
		apicompat.AnthropicStreamEvent{
			Type: "message_stop",
		},
	)

	return events, p.buildResponse(), usage
}

func (p *StreamProcessor) buildUsage() Usage {
	return Usage{
		PromptTokens:     p.promptTokens,
		CompletionTokens: p.completionTokens,
		CachedTokens:     p.cachedTokens,
		CacheWriteTokens: p.cacheWriteTokens,
		ModelName:        p.upstreamModel,
		RequestID:        p.upstreamReqID,
	}
}

func (p *StreamProcessor) buildResponse() *apicompat.AnthropicResponse {
	usage := p.buildUsage()
	stop := p.stopReason
	if stop == "" {
		stop = "end_turn"
	}
	blocks := p.contentBlocks
	if blocks == nil {
		blocks = []apicompat.AnthropicContentBlock{}
	}
	return &apicompat.AnthropicResponse{
		ID:         p.messageID,
		Type:       "message",
		Role:       "assistant",
		Content:    blocks,
		Model:      p.model,
		StopReason: apicompat.AnthropicStopReasonPtr(stop),
		Usage: apicompat.AnthropicUsage{
			InputTokens:              int(usage.PromptTokens),
			OutputTokens:             int(usage.CompletionTokens),
			CacheCreationInputTokens: int(usage.CacheWriteTokens),
			CacheReadInputTokens:     int(usage.CachedTokens),
		},
	}
}

func intPtr(v int) *int {
	return &v
}
