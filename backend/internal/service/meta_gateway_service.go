package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/meta"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	metaDefaultRateLimitWait = 5 * time.Minute
	metaMaxSSEScannerBuf     = 16 * 1024 * 1024
)

// MetaGatewayService proxies Anthropic Messages, OpenAI Chat Completions, and OpenAI Responses
// requests to Meta Muse Code (https://api.meta.ai/v1/responses).
type MetaGatewayService struct {
	accountRepo      AccountRepository
	rateLimitService *RateLimitService
	httpUpstream     HTTPUpstream
	settingService   *SettingService
	metaOAuthService *MetaOAuthService
}

// NewMetaGatewayService constructs a MetaGatewayService.
func NewMetaGatewayService(
	accountRepo AccountRepository,
	rateLimitService *RateLimitService,
	httpUpstream HTTPUpstream,
	settingService *SettingService,
	metaOAuthService *MetaOAuthService,
) *MetaGatewayService {
	return &MetaGatewayService{
		accountRepo:      accountRepo,
		rateLimitService: rateLimitService,
		httpUpstream:     httpUpstream,
		settingService:   settingService,
		metaOAuthService: metaOAuthService,
	}
}

type metaClientProtocol uint8

const (
	metaProtocolAnthropic metaClientProtocol = iota
	metaProtocolChatCompletions
	metaProtocolResponses
)

// Forward handles Anthropic Messages API requests (/v1/messages) for Meta Muse accounts.
func (s *MetaGatewayService) Forward(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	_ bool,
) (*ForwardResult, error) {
	var req apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, s.writeProtocolError(c, metaProtocolAnthropic, http.StatusBadRequest, "invalid_request_error", "Invalid request body")
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, s.writeProtocolError(c, metaProtocolAnthropic, http.StatusBadRequest, "invalid_request_error", "model is required")
	}
	respReq, err := apicompat.AnthropicToResponses(&req)
	if err != nil {
		return nil, s.writeProtocolError(c, metaProtocolAnthropic, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	rawResponses, err := json.Marshal(respReq)
	if err != nil {
		return nil, s.writeProtocolError(c, metaProtocolAnthropic, http.StatusInternalServerError, "api_error", "Failed to encode upstream request")
	}
	return s.forwardUnified(ctx, c, account, rawResponses, metaProtocolAnthropic, req.Model, req.Stream, false)
}

// ForwardAsChatCompletions handles OpenAI Chat Completions requests (/v1/chat/completions).
func (s *MetaGatewayService) ForwardAsChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	_ *ParsedRequest,
) (*ForwardResult, error) {
	var chatReq apicompat.ChatCompletionsRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		return nil, s.writeProtocolError(c, metaProtocolChatCompletions, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(chatReq.Model) == "" {
		return nil, s.writeProtocolError(c, metaProtocolChatCompletions, http.StatusBadRequest, "invalid_request_error", "model is required")
	}
	respReq, err := chatCompletionsToResponses(settingServiceConfig(s.settingService), &chatReq)
	if err != nil {
		return nil, s.writeProtocolError(c, metaProtocolChatCompletions, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	rawResponses, err := json.Marshal(respReq)
	if err != nil {
		return nil, s.writeProtocolError(c, metaProtocolChatCompletions, http.StatusInternalServerError, "api_error", "Failed to encode upstream request")
	}
	includeUsage := chatReq.StreamOptions != nil && chatReq.StreamOptions.IncludeUsage
	return s.forwardUnified(ctx, c, account, rawResponses, metaProtocolChatCompletions, chatReq.Model, chatReq.Stream, includeUsage)
}

// ForwardAsResponses handles OpenAI Responses API requests (/v1/responses).
func (s *MetaGatewayService) ForwardAsResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	_ *ParsedRequest,
) (*ForwardResult, error) {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		return nil, s.writeProtocolError(c, metaProtocolResponses, http.StatusBadRequest, "invalid_request_error", "model is required")
	}
	clientStream := gjson.GetBytes(body, "stream").Bool()
	return s.forwardUnified(ctx, c, account, body, metaProtocolResponses, model, clientStream, false)
}

func (s *MetaGatewayService) forwardUnified(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	rawResponsesBody []byte,
	protocol metaClientProtocol,
	originalModel string,
	clientStream bool,
	includeUsage bool,
) (*ForwardResult, error) {
	startTime := time.Now()
	if account == nil {
		return nil, errors.New("meta account is nil")
	}

	mappedModel := account.GetMappedModel(originalModel)
	if strings.TrimSpace(mappedModel) == "" {
		mappedModel = originalModel
	}
	SetOpsUpstreamModel(c, mappedModel)

	token, err := s.ensureUsableAPIKey(ctx, account, false)
	if err != nil {
		return nil, &UpstreamFailoverError{
			StatusCode:   http.StatusUnauthorized,
			ResponseBody: []byte(err.Error()),
			RetryableOnSameAccount: false,
		}
	}

	payload, err := meta.PrepareResponsesPayload(rawResponsesBody, mappedModel)
	if err != nil {
		return nil, s.writeProtocolError(c, protocol, http.StatusBadRequest, "invalid_request_error", err.Error())
	}

	resp, err := s.doResponsesRequest(ctx, account, token, payload, mappedModel)
	if err != nil {
		return nil, &UpstreamFailoverError{
			StatusCode:   http.StatusBadGateway,
			ResponseBody: []byte(err.Error()),
		}
	}

	// On 401, if the account carries a DCA token, try minting a fresh API key once.
	if resp.StatusCode == http.StatusUnauthorized && account.GetMetaDCAToken() != "" {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if freshToken, mintErr := s.ensureUsableAPIKey(ctx, account, true); mintErr == nil && freshToken != "" && freshToken != token {
			resp, err = s.doResponsesRequest(ctx, account, freshToken, payload, mappedModel)
			if err != nil {
				return nil, &UpstreamFailoverError{
					StatusCode:   http.StatusBadGateway,
					ResponseBody: []byte(err.Error()),
				}
			}
		} else {
			s.markAccountError(ctx, account, "Meta Muse authentication failed (401)")
			return nil, &UpstreamFailoverError{
				StatusCode:   http.StatusUnauthorized,
				ResponseBody: []byte("meta authentication failed"),
			}
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return s.handleUpstreamHTTPError(ctx, c, account, protocol, resp.StatusCode, errBody)
	}

	if clientStream {
		return s.handleStreaming(ctx, c, resp, protocol, originalModel, mappedModel, includeUsage, startTime)
	}
	return s.handleNonStreaming(c, resp, protocol, originalModel, mappedModel, startTime)
}

func (s *MetaGatewayService) ensureUsableAPIKey(ctx context.Context, account *Account, forceMint bool) (string, error) {
	if !forceMint {
		if key := account.GetMetaAPIKey(); key != "" {
			return key, nil
		}
	}
	if dca := account.GetMetaDCAToken(); dca != "" && s.metaOAuthService != nil {
		info, err := s.metaOAuthService.RefreshAccountToken(ctx, account)
		if err != nil {
			return "", fmt.Errorf("meta mint api key failed: %w", err)
		}
		if strings.TrimSpace(info.APIKey) == "" {
			return "", errors.New("meta mint returned empty api_key")
		}
		newCreds := MergeCredentials(account.Credentials, s.metaOAuthService.BuildAccountCredentials(info))
		account.Credentials = newCreds
		if s.accountRepo != nil && account.ID > 0 {
			if err := s.accountRepo.Update(ctx, account); err != nil {
				slog.Warn("meta_persist_minted_key_failed", "account_id", account.ID, "error", err)
			}
		}
		return info.APIKey, nil
	}
	return "", errors.New("meta account missing api_key and dca_token")
}

func (s *MetaGatewayService) doResponsesRequest(
	ctx context.Context,
	account *Account,
	token string,
	payload []byte,
	model string,
) (*http.Response, error) {
	endpoint := meta.ResponsesEndpointURL(account.GetMetaBaseURL())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build meta request: %w", err)
	}
	meta.ApplyMetaAPIHeaders(req, token, true, model)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	return s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
}

func (s *MetaGatewayService) handleUpstreamHTTPError(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	protocol metaClientProtocol,
	statusCode int,
	body []byte,
) (*ForwardResult, error) {
	now := time.Now()
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		s.markAccountError(ctx, account, fmt.Sprintf("Meta Muse HTTP %d: %s", statusCode, truncateString(string(body), 200)))
		return nil, &UpstreamFailoverError{StatusCode: statusCode, ResponseBody: body}
	case http.StatusTooManyRequests:
		resetAt, _ := meta.ParseRateLimitResetAt(statusCode, body, now)
		if resetAt.IsZero() {
			resetAt = now.Add(metaDefaultRateLimitWait)
		}
		if s.accountRepo != nil && account.ID > 0 {
			_ = s.accountRepo.SetRateLimited(ctx, account.ID, resetAt)
		}
		return nil, &UpstreamFailoverError{StatusCode: statusCode, ResponseBody: body}
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return nil, &UpstreamFailoverError{StatusCode: statusCode, ResponseBody: body}
	default:
		msg := strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		if msg == "" {
			msg = fmt.Sprintf("Meta Muse upstream returned HTTP %d", statusCode)
		}
		return nil, s.writeProtocolError(c, protocol, statusCode, "api_error", msg)
	}
}

func (s *MetaGatewayService) markAccountError(ctx context.Context, account *Account, msg string) {
	if s.accountRepo == nil || account == nil || account.ID <= 0 {
		return
	}
	if err := s.accountRepo.SetError(ctx, account.ID, msg); err != nil {
		slog.Warn("meta_set_account_error_failed", "account_id", account.ID, "error", err)
	}
}

func (s *MetaGatewayService) handleStreaming(
	ctx context.Context,
	c *gin.Context,
	resp *http.Response,
	protocol metaClientProtocol,
	originalModel string,
	mappedModel string,
	includeUsage bool,
	startTime time.Time,
) (*ForwardResult, error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	flusher, _ := c.Writer.(http.Flusher)

	var (
		anthState *apicompat.ResponsesEventToAnthropicState
		chatState *apicompat.ResponsesEventToChatState
	)
	switch protocol {
	case metaProtocolAnthropic:
		anthState = apicompat.NewResponsesEventToAnthropicState()
		anthState.Model = originalModel
	case metaProtocolChatCompletions:
		chatState = apicompat.NewResponsesEventToChatState()
		chatState.Model = originalModel
		chatState.IncludeUsage = includeUsage
	}

	var (
		firstTokenMs *int
		usage        ClaudeUsage
		eventName    string
		skipEvent    bool
	)

	recordFirstToken := func() {
		if firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), metaMaxSSEScannerBuf)

	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			eventName = ""
			skipEvent = false
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			skipEvent = meta.IsSubscriptionUsageEvent(eventName, nil)
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if dataStr == "" || dataStr == "[DONE]" {
			continue
		}
		rawData := []byte(dataStr)
		if skipEvent || meta.IsSubscriptionUsageEvent(eventName, rawData) {
			continue
		}

		var evt apicompat.ResponsesStreamEvent
		if err := json.Unmarshal(rawData, &evt); err != nil {
			continue
		}
		if evt.Type == "" && eventName != "" {
			evt.Type = eventName
		}
		if evt.Response != nil && evt.Response.Usage != nil {
			mergeMetaUsage(&usage, evt.Response.Usage)
		}

		switch protocol {
		case metaProtocolResponses:
			recordFirstToken()
			evType := evt.Type
			if evType != "" {
				_, _ = fmt.Fprintf(c.Writer, "event: %s\n", evType)
			}
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", dataStr)
			if flusher != nil {
				flusher.Flush()
			}
		case metaProtocolChatCompletions:
			chunks := apicompat.ResponsesEventToChatChunks(&evt, chatState)
			for _, chunk := range chunks {
				recordFirstToken()
				sse, err := apicompat.ChatChunkToSSE(chunk)
				if err != nil {
					continue
				}
				_, _ = io.WriteString(c.Writer, sse)
			}
			if len(chunks) > 0 && flusher != nil {
				flusher.Flush()
			}
		case metaProtocolAnthropic:
			events := apicompat.ResponsesEventToAnthropicEvents(&evt, anthState)
			for _, aEvt := range events {
				recordFirstToken()
				sse, err := apicompat.ResponsesAnthropicEventToSSE(aEvt)
				if err != nil {
					continue
				}
				_, _ = io.WriteString(c.Writer, sse)
			}
			if len(events) > 0 && flusher != nil {
				flusher.Flush()
			}
		}
	}

	switch protocol {
	case metaProtocolChatCompletions:
		for _, chunk := range apicompat.FinalizeResponsesChatStream(chatState) {
			if sse, err := apicompat.ChatChunkToSSE(chunk); err == nil {
				_, _ = io.WriteString(c.Writer, sse)
			}
		}
		_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	case metaProtocolAnthropic:
		for _, aEvt := range apicompat.FinalizeResponsesAnthropicStream(anthState) {
			if sse, err := apicompat.ResponsesAnthropicEventToSSE(aEvt); err == nil {
				_, _ = io.WriteString(c.Writer, sse)
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	return &ForwardResult{
		RequestID:     resp.Header.Get("x-request-id"),
		Usage:         usage,
		Model:         originalModel,
		UpstreamModel: mappedModel,
		Stream:        true,
		Duration:      time.Since(startTime),
		FirstTokenMs:  firstTokenMs,
	}, nil
}

func (s *MetaGatewayService) handleNonStreaming(
	c *gin.Context,
	resp *http.Response,
	protocol metaClientProtocol,
	originalModel string,
	mappedModel string,
	startTime time.Time,
) (*ForwardResult, error) {
	finalResp, usage, err := collectMetaSSEResponse(resp.Body, originalModel)
	if err != nil {
		return nil, s.writeProtocolError(c, protocol, http.StatusBadGateway, "api_error", err.Error())
	}

	var outPayload any
	switch protocol {
	case metaProtocolResponses:
		outPayload = finalResp
	case metaProtocolChatCompletions:
		outPayload = apicompat.ResponsesToChatCompletions(finalResp, originalModel)
	case metaProtocolAnthropic:
		outPayload = apicompat.ResponsesToAnthropic(finalResp, originalModel)
	}

	c.JSON(http.StatusOK, outPayload)

	return &ForwardResult{
		RequestID:     resp.Header.Get("x-request-id"),
		Usage:         usage,
		Model:         originalModel,
		UpstreamModel: mappedModel,
		Stream:        false,
		Duration:      time.Since(startTime),
	}, nil
}

func collectMetaSSEResponse(body io.Reader, fallbackModel string) (*apicompat.ResponsesResponse, ClaudeUsage, error) {
	acc := apicompat.NewBufferedResponseAccumulator()
	var (
		finalResp *apicompat.ResponsesResponse
		usage     ClaudeUsage
		eventName string
		skipEvent bool
	)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), metaMaxSSEScannerBuf)

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			eventName = ""
			skipEvent = false
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			skipEvent = meta.IsSubscriptionUsageEvent(eventName, nil)
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if dataStr == "" || dataStr == "[DONE]" {
			continue
		}
		rawData := []byte(dataStr)
		if skipEvent || meta.IsSubscriptionUsageEvent(eventName, rawData) {
			continue
		}

		var evt apicompat.ResponsesStreamEvent
		if err := json.Unmarshal(rawData, &evt); err != nil {
			continue
		}
		if evt.Type == "" && eventName != "" {
			evt.Type = eventName
		}
		acc.ProcessEvent(&evt)
		if evt.Response != nil && evt.Response.Usage != nil {
			mergeMetaUsage(&usage, evt.Response.Usage)
		}
		if (evt.Type == "response.completed" || evt.Type == "response.incomplete") && evt.Response != nil {
			respCopy := *evt.Response
			finalResp = &respCopy
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, usage, fmt.Errorf("read meta SSE stream: %w", err)
	}
	if finalResp == nil {
		if !acc.HasContent() {
			return nil, usage, errors.New("meta upstream returned empty response stream")
		}
		finalResp = &apicompat.ResponsesResponse{
			Object: "response",
			Status: "completed",
			Model:  fallbackModel,
			Output: acc.BuildOutput(),
		}
	} else {
		acc.SupplementResponseOutput(finalResp)
	}
	if finalResp.Model == "" {
		finalResp.Model = fallbackModel
	}
	return finalResp, usage, nil
}

func mergeMetaUsage(dst *ClaudeUsage, src *apicompat.ResponsesUsage) {
	if dst == nil || src == nil {
		return
	}
	if src.InputTokens > 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.InputTokensDetails != nil && src.InputTokensDetails.CachedTokens > 0 {
		dst.CacheReadInputTokens = src.InputTokensDetails.CachedTokens
	}
}

func (s *MetaGatewayService) writeProtocolError(
	c *gin.Context,
	protocol metaClientProtocol,
	status int,
	errType string,
	message string,
) error {
	if c != nil && !c.Writer.Written() {
		switch protocol {
		case metaProtocolAnthropic:
			c.JSON(status, gin.H{
				"type": "error",
				"error": gin.H{
					"type":    errType,
					"message": message,
				},
			})
		default:
			c.JSON(status, gin.H{
				"error": gin.H{
					"type":    errType,
					"message": message,
				},
			})
		}
	}
	return errors.New(message)
}

// TestConnectionWithPrompt runs a lightweight Responses request to verify Meta Muse credentials.
func (s *MetaGatewayService) TestConnectionWithPrompt(
	ctx context.Context,
	account *Account,
	modelID string,
	prompt string,
) (string, error) {
	if strings.TrimSpace(modelID) == "" {
		modelID = meta.DefaultTestModel
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Reply with OK."
	}
	token, err := s.ensureUsableAPIKey(ctx, account, false)
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(map[string]any{
		"model":  modelID,
		"stream": true,
		"input": []map[string]any{
			{"role": "user", "content": prompt},
		},
	})
	payload, err := meta.PrepareResponsesPayload(raw, modelID)
	if err != nil {
		return "", err
	}
	resp, err := s.doResponsesRequest(ctx, account, token, payload, modelID)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("meta test failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	finalResp, _, err := collectMetaSSEResponse(resp.Body, modelID)
	if err != nil {
		return "", err
	}
	chat := apicompat.ResponsesToChatCompletions(finalResp, modelID)
	if chat != nil && len(chat.Choices) > 0 && len(chat.Choices[0].Message.Content) > 0 {
		var text string
		if err := json.Unmarshal(chat.Choices[0].Message.Content, &text); err == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text), nil
		}
	}
	return "OK", nil
}

// FetchModels returns Meta Muse's curated model list.
func (s *MetaGatewayService) FetchModels(_ context.Context, _ *Account) ([]string, error) {
	return meta.DefaultModelIDs(), nil
}
