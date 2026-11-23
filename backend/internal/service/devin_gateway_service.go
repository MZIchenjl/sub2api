package service

import (
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
	"github.com/Wei-Shaw/sub2api/internal/pkg/devin"
	"github.com/gin-gonic/gin"
)

const (
	devinDefaultDailyCooldown  = 1 * time.Hour
	devinDefaultWeeklyCooldown = 6 * time.Hour
	devinDefaultRateLimitWait  = 30 * time.Second
)

// DevinGatewayService proxies Anthropic Messages, OpenAI Chat Completions, and OpenAI Responses
// requests through Devin's Connect-RPC GetChatMessage backend.
type DevinGatewayService struct {
	accountRepo      AccountRepository
	rateLimitService *RateLimitService
	httpUpstream     HTTPUpstream
	settingService   *SettingService
}

// NewDevinGatewayService creates a new DevinGatewayService instance.
func NewDevinGatewayService(
	accountRepo AccountRepository,
	rateLimitService *RateLimitService,
	httpUpstream HTTPUpstream,
	settingService *SettingService,
) *DevinGatewayService {
	return &DevinGatewayService{
		accountRepo:      accountRepo,
		rateLimitService: rateLimitService,
		httpUpstream:     httpUpstream,
		settingService:   settingService,
	}
}

type devinClientProtocol uint8

const (
	devinProtocolAnthropic devinClientProtocol = iota
	devinProtocolChatCompletions
	devinProtocolResponses
)

// Forward handles Anthropic Messages API requests (/v1/messages) for Devin accounts.
func (s *DevinGatewayService) Forward(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	_ bool,
) (*ForwardResult, error) {
	var req apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, s.writeProtocolError(c, devinProtocolAnthropic, http.StatusBadRequest, "invalid_request_error", "Invalid request body")
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, s.writeProtocolError(c, devinProtocolAnthropic, http.StatusBadRequest, "invalid_request_error", "model is required")
	}

	return s.forwardUnified(ctx, c, account, &req, devinProtocolAnthropic, req.Model, req.Stream, false, "")
}

// ForwardAsChatCompletions handles OpenAI Chat Completions requests (/v1/chat/completions) for Devin accounts.
func (s *DevinGatewayService) ForwardAsChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	_ *ParsedRequest,
) (*ForwardResult, error) {
	var chatReq apicompat.ChatCompletionsRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		return nil, s.writeProtocolError(c, devinProtocolChatCompletions, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(chatReq.Model) == "" {
		return nil, s.writeProtocolError(c, devinProtocolChatCompletions, http.StatusBadRequest, "invalid_request_error", "model is required")
	}

	responsesReq, err := chatCompletionsToResponses(settingServiceConfig(s.settingService), &chatReq)
	if err != nil {
		return nil, s.writeProtocolError(c, devinProtocolChatCompletions, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	anthropicReq, err := apicompat.ResponsesToAnthropicRequest(responsesReq)
	if err != nil {
		return nil, s.writeProtocolError(c, devinProtocolChatCompletions, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	preserveChatCompletionTokenLimit(&chatReq, anthropicReq)
	anthropicReq.Stream = chatReq.Stream

	includeUsage := chatReq.StreamOptions != nil && chatReq.StreamOptions.IncludeUsage
	effort := ""
	if ptr := extractCCReasoningEffortFromBody(body); ptr != nil {
		effort = *ptr
	}
	return s.forwardUnified(ctx, c, account, anthropicReq, devinProtocolChatCompletions, chatReq.Model, chatReq.Stream, includeUsage, effort)
}

// ForwardAsResponses handles OpenAI Responses API requests (/v1/responses) for Devin accounts.
func (s *DevinGatewayService) ForwardAsResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	_ *ParsedRequest,
) (*ForwardResult, error) {
	var respReq apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &respReq); err != nil {
		return nil, s.writeProtocolError(c, devinProtocolResponses, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(respReq.Model) == "" {
		return nil, s.writeProtocolError(c, devinProtocolResponses, http.StatusBadRequest, "invalid_request_error", "model is required")
	}

	anthropicReq, err := apicompat.ResponsesToAnthropicRequest(&respReq)
	if err != nil {
		return nil, s.writeProtocolError(c, devinProtocolResponses, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	anthropicReq.Stream = respReq.Stream

	effort := ""
	if ptr := ExtractResponsesReasoningEffortFromBody(body); ptr != nil {
		effort = *ptr
	}
	return s.forwardUnified(ctx, c, account, anthropicReq, devinProtocolResponses, respReq.Model, respReq.Stream, false, effort)
}

// TestConnectionWithPrompt executes a minimal GetChatMessage call to test a Devin account.
func (s *DevinGatewayService) TestConnectionWithPrompt(ctx context.Context, account *Account, modelID, prompt string) (*TestConnectionResult, error) {
	if account == nil {
		return nil, errors.New("account is required")
	}
	token := account.GetDevinToken()
	if token == "" {
		return nil, errors.New("devin session token or API key is missing")
	}
	testModel := strings.TrimSpace(modelID)
	if testModel == "" {
		testModel = devin.DefaultTestModel
	}
	mappedModel := account.GetMappedModel(testModel)
	if mappedModel == "" {
		mappedModel = testModel
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Reply with OK."
	}

	msgJSON, _ := json.Marshal(prompt)
	anthropicReq := &apicompat.AnthropicRequest{
		Model:     mappedModel,
		MaxTokens: 256,
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: msgJSON},
		},
	}

	chatParams, err := devin.AnthropicRequestToDevin(anthropicReq, mappedModel, devin.TranslateOptions{})
	if err != nil {
		return nil, err
	}

	resp, err := s.doUpstreamCall(ctx, account, token, chatParams)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("devin upstream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	proc := devin.NewStreamProcessor(mappedModel)
	for {
		flag, payload, err := devin.ReadConnectFrame(resp.Body)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read devin stream: %w", err)
		}
		if flag&devin.ConnectFlagEndStream != 0 {
			if _, _, _, trailerErr := devin.ParseTrailerError(payload); trailerErr != nil {
				return nil, trailerErr
			}
			break
		}
		frame, err := devin.ParseFrame(payload)
		if err != nil {
			continue
		}
		proc.ProcessFrame(frame)
	}

	_, finalResp, _ := proc.Finalize()
	var text strings.Builder
	for _, b := range finalResp.Content {
		if b.Type == "text" && b.Text != "" {
			text.WriteString(b.Text)
		}
	}
	return &TestConnectionResult{
		Text:        text.String(),
		MappedModel: chatParams.ModelUID,
	}, nil
}

// FetchModels returns account-visible Devin models via GetCliModelConfigs, falling back to DefaultModels.
func (s *DevinGatewayService) FetchModels(ctx context.Context, account *Account) ([]devin.ModelConfig, error) {
	if account == nil {
		return devin.DefaultModels(), nil
	}
	token := account.GetDevinToken()
	if token == "" {
		return devin.DefaultModels(), nil
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	client, err := devin.NewClientWithTimeout(proxyURL, 15*time.Second)
	if err != nil {
		return devin.DefaultModels(), nil
	}
	client.SetServerBaseURL(account.GetDevinBaseURL())
	models, err := client.FetchCliModelConfigs(ctx, token, fmt.Sprintf("acct-%d", account.ID))
	if err != nil || len(models) == 0 {
		return devin.DefaultModels(), nil
	}
	return models, nil
}

func (s *DevinGatewayService) forwardUnified(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	anthropicReq *apicompat.AnthropicRequest,
	protocol devinClientProtocol,
	originalModel string,
	clientStream bool,
	includeUsage bool,
	requestedEffort string,
) (*ForwardResult, error) {
	startTime := time.Now()
	if account == nil {
		return nil, s.writeProtocolError(c, protocol, http.StatusBadGateway, "api_error", "Devin account is missing")
	}

	token := account.GetDevinToken()
	if token == "" {
		return nil, &UpstreamFailoverError{
			StatusCode:   http.StatusUnauthorized,
			ResponseBody: []byte(`{"error":{"type":"authentication_error","message":"Devin session token is missing"}}`),
		}
	}

	// Enforce account model whitelist / mapping when configured.
	mappedModel := account.GetMappedModel(originalModel)
	if !account.IsModelSupported(originalModel) {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		return nil, s.writeProtocolError(c, protocol, http.StatusForbidden, "permission_error", fmt.Sprintf("model %s not in whitelist", originalModel))
	}
	if mappedModel == "" {
		mappedModel = originalModel
	}

	sessionID := getSessionID(c)
	if sessionID == "" {
		sessionID = ExtractClientSessionID(c)
	}

	// Allow a single automatic retry stripping cross-provider thinking signatures when Devin rejects them.
	for attempt := 0; attempt < 2; attempt++ {
		stripSignatures := attempt > 0
		chatParams, err := devin.AnthropicRequestToDevin(anthropicReq, mappedModel, devin.TranslateOptions{
			SessionID:       sessionID,
			CascadeID:       sessionID,
			StripSignatures: stripSignatures,
			RequestedEffort: requestedEffort,
		})
		if err != nil {
			return nil, s.writeProtocolError(c, protocol, http.StatusBadRequest, "invalid_request_error", err.Error())
		}

		resp, err := s.doUpstreamCall(ctx, account, token, chatParams)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return nil, s.writeProtocolError(c, protocol, 499, "client_disconnected", "Client disconnected before upstream response")
			}
			return nil, &UpstreamFailoverError{
				StatusCode:   http.StatusBadGateway,
				ResponseBody: []byte(fmt.Sprintf(`{"error":{"type":"upstream_error","message":%q}}`, err.Error())),
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
			_ = resp.Body.Close()
			if attempt == 0 && resp.StatusCode == http.StatusBadRequest && devin.IsRequestIssueError(string(errBody)) {
				continue
			}
			return nil, s.handleUpstreamFailure(ctx, c, account, protocol, resp.StatusCode, string(errBody))
		}

		result, retryUnsigned, err := s.consumeAndRelayStream(ctx, c, account, resp, protocol, originalModel, chatParams.ModelUID, clientStream, includeUsage, startTime, attempt == 0)
		_ = resp.Body.Close()
		if retryUnsigned {
			continue
		}
		return result, err
	}

	return nil, &UpstreamFailoverError{
		StatusCode:   http.StatusBadGateway,
		ResponseBody: []byte(`{"error":{"type":"upstream_error","message":"Devin request failed"}}`),
	}
}

func (s *DevinGatewayService) doUpstreamCall(ctx context.Context, account *Account, token string, params *devin.ChatRequestParams) (*http.Response, error) {
	deviceSeed := fmt.Sprintf("sub2api-devin-%d", account.ID)
	protoBytes := devin.BuildGetChatMessageRequest(token, deviceSeed, params)
	httpReq, err := devin.NewChatHTTPRequest(ctx, account.GetDevinBaseURL(), token, protoBytes)
	if err != nil {
		return nil, err
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	if s.httpUpstream != nil {
		return s.httpUpstream.Do(httpReq, proxyURL, account.ID, account.Concurrency)
	}
	client, err := devin.NewClientWithTimeout(proxyURL, 0)
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{}
	_ = client
	return httpClient.Do(httpReq)
}

func (s *DevinGatewayService) consumeAndRelayStream(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	resp *http.Response,
	protocol devinClientProtocol,
	originalModel string,
	upstreamModelUID string,
	clientStream bool,
	includeUsage bool,
	startTime time.Time,
	canRetryUnsigned bool,
) (*ForwardResult, bool, error) {
	proc := devin.NewStreamProcessor(originalModel)

	var (
		streamOpened   bool
		firstTokenMs   *int
		anthropicState *apicompat.AnthropicEventToResponsesState
		chatState      *apicompat.ResponsesEventToChatState
	)
	if clientStream {
		switch protocol {
		case devinProtocolChatCompletions:
			anthropicState = apicompat.NewAnthropicEventToResponsesState()
			anthropicState.Model = originalModel
			chatState = apicompat.NewResponsesEventToChatState()
			chatState.Model = originalModel
			chatState.IncludeUsage = includeUsage
		case devinProtocolResponses:
			anthropicState = apicompat.NewAnthropicEventToResponsesState()
			anthropicState.Model = originalModel
		}
	}

	openSSE := func() {
		if streamOpened {
			return
		}
		streamOpened = true
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)
		c.Writer.Flush()
	}

	writeEvent := func(ev apicompat.AnthropicStreamEvent) {
		if !clientStream {
			return
		}
		openSSE()
		switch protocol {
		case devinProtocolAnthropic:
			if data, err := json.Marshal(ev); err == nil {
				_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", ev.Type, data)
				c.Writer.Flush()
			}
		case devinProtocolResponses:
			for _, re := range apicompat.AnthropicEventToResponsesEvents(&ev, anthropicState) {
				if data, err := apicompat.ResponsesEventToSSE(re); err == nil {
					_, _ = c.Writer.Write([]byte(data))
					c.Writer.Flush()
				}
			}
		case devinProtocolChatCompletions:
			for _, re := range apicompat.AnthropicEventToResponsesEvents(&ev, anthropicState) {
				for _, chunk := range apicompat.ResponsesEventToChatChunks(&re, chatState) {
					if data, err := apicompat.ChatChunkToSSE(chunk); err == nil {
						_, _ = c.Writer.Write([]byte(data))
						c.Writer.Flush()
					}
				}
			}
		}
	}

	// Buffer events until meaningful content arrives so pre-content trailer errors can still failover cleanly.
	var pendingEvents []apicompat.AnthropicStreamEvent
	flushPending := func() {
		for _, ev := range pendingEvents {
			writeEvent(ev)
		}
		pendingEvents = nil
	}

	for {
		flag, payload, err := devin.ReadConnectFrame(resp.Body)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if !streamOpened {
				return nil, false, &UpstreamFailoverError{
					StatusCode:   http.StatusBadGateway,
					ResponseBody: []byte(fmt.Sprintf(`{"error":{"type":"upstream_error","message":%q}}`, err.Error())),
				}
			}
			break
		}

		if flag&devin.ConnectFlagEndStream != 0 {
			status, _, msg, trailerErr := devin.ParseTrailerError(payload)
			if trailerErr != nil {
				if !streamOpened {
					if canRetryUnsigned && status == http.StatusBadRequest && devin.IsRequestIssueError(msg) {
						return nil, true, nil
					}
					return nil, false, s.handleUpstreamFailure(ctx, c, account, protocol, status, msg)
				}
				// Stream already opened to client; emit SSE error event.
				s.writeStreamError(c, protocol, msg)
				return nil, false, trailerErr
			}
			break
		}

		frame, err := devin.ParseFrame(payload)
		if err != nil {
			continue
		}
		events := proc.ProcessFrame(frame)
		if len(events) == 0 {
			continue
		}
		if proc.HasMeaningfulContent() {
			if firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
			flushPending()
			for _, ev := range events {
				writeEvent(ev)
			}
		} else {
			pendingEvents = append(pendingEvents, events...)
		}
	}

	finalEvents, anthropicResp, usage := proc.Finalize()
	if !proc.HasMeaningfulContent() {
		if !streamOpened {
			return nil, false, &UpstreamFailoverError{
				StatusCode:   http.StatusBadGateway,
				ResponseBody: []byte(`{"error":{"type":"upstream_error","message":"Devin returned empty response"}}`),
			}
		}
		s.writeStreamError(c, protocol, "Devin returned empty response")
		return nil, false, errors.New("devin returned empty response")
	}

	if clientStream {
		flushPending()
		for _, ev := range finalEvents {
			writeEvent(ev)
		}
		if protocol == devinProtocolResponses {
			for _, re := range apicompat.FinalizeAnthropicResponsesStream(anthropicState) {
				if data, err := apicompat.ResponsesEventToSSE(re); err == nil {
					_, _ = c.Writer.Write([]byte(data))
					c.Writer.Flush()
				}
			}
		} else if protocol == devinProtocolChatCompletions {
			for _, re := range apicompat.FinalizeAnthropicResponsesStream(anthropicState) {
				for _, chunk := range apicompat.ResponsesEventToChatChunks(&re, chatState) {
					if data, err := apicompat.ChatChunkToSSE(chunk); err == nil {
						_, _ = c.Writer.Write([]byte(data))
						c.Writer.Flush()
					}
				}
			}
			for _, chunk := range apicompat.FinalizeResponsesChatStream(chatState) {
				if data, err := apicompat.ChatChunkToSSE(chunk); err == nil {
					_, _ = c.Writer.Write([]byte(data))
					c.Writer.Flush()
				}
			}
			_, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
			c.Writer.Flush()
		}
	} else {
		switch protocol {
		case devinProtocolAnthropic:
			c.JSON(http.StatusOK, anthropicResp)
		case devinProtocolResponses:
			c.JSON(http.StatusOK, apicompat.AnthropicToResponsesResponse(anthropicResp))
		case devinProtocolChatCompletions:
			respObj := apicompat.AnthropicToResponsesResponse(anthropicResp)
			c.JSON(http.StatusOK, apicompat.ResponsesToChatCompletions(respObj, originalModel))
		}
	}

	upstreamModel := upstreamModelUID
	if upstreamModel == originalModel {
		upstreamModel = ""
	}
	return &ForwardResult{
		RequestID:             usage.RequestID,
		UpstreamHeaders:       resp.Header.Clone(),
		Model:                 originalModel,
		UpstreamModel:         upstreamModel,
		UpstreamResponseModel: usage.ModelName,
		Stream:                clientStream,
		Duration:              time.Since(startTime),
		FirstTokenMs:          firstTokenMs,
		Usage: ClaudeUsage{
			InputTokens:              int(usage.PromptTokens),
			OutputTokens:             int(usage.CompletionTokens),
			CacheCreationInputTokens: int(usage.CacheWriteTokens),
			CacheReadInputTokens:     int(usage.CachedTokens),
		},
	}, false, nil
}

func (s *DevinGatewayService) handleUpstreamFailure(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	protocol devinClientProtocol,
	statusCode int,
	message string,
) error {
	trimmedMsg := strings.TrimSpace(message)
	if trimmedMsg == "" {
		trimmedMsg = http.StatusText(statusCode)
	}

	// 1. Account-wide daily/weekly quota exhaustion -> cooldown until resetAt and failover.
	if period := devin.QuotaExhaustionPeriod(trimmedMsg); period != devin.QuotaPeriodNone {
		resetAt := s.resolveQuotaCooldownResetAt(account, period)
		if s.accountRepo != nil {
			if err := s.accountRepo.SetRateLimited(ctx, account.ID, resetAt); err != nil {
				slog.Warn("devin_set_rate_limited_failed", "account_id", account.ID, "error", err)
			}
		}
		return &UpstreamFailoverError{
			StatusCode:   http.StatusTooManyRequests,
			ResponseBody: []byte(fmt.Sprintf(`{"error":{"type":"rate_limit_error","message":%q}}`, trimmedMsg)),
		}
	}

	// 2. Authentication rejected (401) -> mark account error and failover.
	if statusCode == http.StatusUnauthorized {
		if s.accountRepo != nil {
			_ = s.accountRepo.SetError(ctx, account.ID, "Devin authentication failed (401): please reauthorize or update token")
		}
		return &UpstreamFailoverError{
			StatusCode:   http.StatusUnauthorized,
			ResponseBody: []byte(fmt.Sprintf(`{"error":{"type":"authentication_error","message":%q}}`, trimmedMsg)),
		}
	}

	// 3. Rate limit / high demand (429) -> short cooldown and failover.
	if statusCode == http.StatusTooManyRequests {
		if s.accountRepo != nil {
			_ = s.accountRepo.SetRateLimited(ctx, account.ID, time.Now().Add(devinDefaultRateLimitWait))
		}
		return &UpstreamFailoverError{
			StatusCode:   http.StatusTooManyRequests,
			ResponseBody: []byte(fmt.Sprintf(`{"error":{"type":"rate_limit_error","message":%q}}`, trimmedMsg)),
		}
	}

	// 4. Upstream 5xx / 403 -> failover.
	if statusCode >= 500 || statusCode == http.StatusForbidden {
		return &UpstreamFailoverError{
			StatusCode:   statusCode,
			ResponseBody: []byte(fmt.Sprintf(`{"error":{"type":"upstream_error","message":%q}}`, trimmedMsg)),
		}
	}

	// 5. Client 4xx errors -> return directly to client without switching accounts.
	return s.writeProtocolError(c, protocol, statusCode, "invalid_request_error", trimmedMsg)
}

func (s *DevinGatewayService) resolveQuotaCooldownResetAt(account *Account, period devin.QuotaPeriod) time.Time {
	now := time.Now()
	if account != nil && account.Extra != nil {
		var key string
		if period == devin.QuotaPeriodWeekly {
			key = "devin_weekly_quota_reset_at"
		} else {
			key = "devin_daily_quota_reset_at"
		}
		if raw := strings.TrimSpace(account.GetExtraString(key)); raw != "" {
			if parsed, err := time.Parse(time.RFC3339, raw); err == nil && parsed.After(now) && parsed.Sub(now) <= 8*24*time.Hour {
				return parsed
			}
		}
	}
	if period == devin.QuotaPeriodWeekly {
		return now.Add(devinDefaultWeeklyCooldown)
	}
	return now.Add(devinDefaultDailyCooldown)
}

func (s *DevinGatewayService) writeProtocolError(c *gin.Context, protocol devinClientProtocol, statusCode int, errType, message string) error {
	if c != nil {
		if protocol == devinProtocolAnthropic {
			c.JSON(statusCode, gin.H{
				"type": "error",
				"error": gin.H{
					"type":    errType,
					"message": message,
				},
			})
		} else {
			c.JSON(statusCode, gin.H{
				"error": gin.H{
					"type":    errType,
					"message": message,
				},
			})
		}
	}
	return fmt.Errorf("%s (%d): %s", errType, statusCode, message)
}

func (s *DevinGatewayService) writeStreamError(c *gin.Context, protocol devinClientProtocol, message string) {
	if c == nil {
		return
	}
	switch protocol {
	case devinProtocolAnthropic, devinProtocolResponses:
		_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"upstream_error\",\"message\":%q}}\n\n", message)
	case devinProtocolChatCompletions:
		_, _ = fmt.Fprintf(c.Writer, "data: {\"error\":{\"message\":%q,\"type\":\"upstream_error\"}}\n\n", message)
	}
	c.Writer.Flush()
}
