package devin

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// ConnectFlagData marks an uncompressed Connect-proto data frame.
	ConnectFlagData byte = 0x00
	// ConnectFlagCompressed marks a gzipped Connect-proto frame.
	ConnectFlagCompressed byte = 0x01
	// ConnectFlagEndStream marks the terminal Connect-proto trailer frame.
	ConnectFlagEndStream byte = 0x02

	// GetChatMessagePath is the streaming Connect-RPC endpoint for chat completions.
	GetChatMessagePath = "/exa.api_server_pb.ApiServerService/GetChatMessage"

	// DefaultMaxTokens is the fallback max completion tokens when omitted.
	DefaultMaxTokens = 64000

	// gzipRequestThreshold matches Devin CLI behavior: compress requests > 8 KiB to avoid 413 on large contexts.
	gzipRequestThreshold     = 8 * 1024
	maxConnectFrameSize      = 16 * 1024 * 1024
	maxDecompressedFrameSize = 64 * 1024 * 1024
)

// PromptSource enumerates ChatMessagePrompt.source values.
const (
	PromptSourceUser      = 1
	PromptSourceAssistant = 2
	PromptSourceTool      = 4
)

// QuotaPeriod identifies an account-wide Devin usage quota exhaustion window.
type QuotaPeriod int

const (
	QuotaPeriodNone QuotaPeriod = iota
	QuotaPeriodDaily
	QuotaPeriodWeekly
)

// Tool represents a tool definition in GetChatMessageRequest (Field 10).
type Tool struct {
	Name        string
	Description string
	Parameters  []byte
}

// ToolCall represents a tool invocation on an assistant prompt (Prompt Field 6).
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ToolCallDelta represents a streaming tool call chunk from response Field 6.
type ToolCallDelta struct {
	ID               string
	Name             string
	Arguments        string
	InvalidJSONStr   string
	InvalidJSONErr   string
	IsCustomToolCall bool
}

// Image represents a base64 image attachment in a Prompt (Prompt Field 10).
type Image struct {
	Base64Data string
	MimeType   string
}

// Prompt represents a single ChatMessagePrompt entry in GetChatMessageRequest (Field 3).
type Prompt struct {
	MessageID     string
	Source        int // PromptSourceUser / PromptSourceAssistant / PromptSourceTool
	Content       string
	Images        []Image
	ToolCalls     []ToolCall
	ToolCallID    string
	CacheControl  bool // emits Field 8: prompt_cache_options { type: EPHEMERAL (1) }
	Thinking      string
	Signature     []byte
	SignatureType string
}

// Usage captures token accounting parsed from response Field 7 and/or Field 28.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	CacheWriteTokens int64
	StatusCode       uint64
	RequestID        string
	ModelName        string
}

// FrameResult holds decoded fields from one Connect-proto response frame.
type FrameResult struct {
	OutputID                string
	Timestamp               uint64
	ContentText             string
	DeltaTokens             uint64
	StopReason              uint64 // 2/4=stop, 3=max_tokens, 10=tool_calls
	ToolCallDeltas          []ToolCallDelta
	ThinkingText            string
	DeltaSignature          []byte
	DeltaSignatureType      string
	MessageID               string
	Usage                   *Usage
	ResponseDimensionGroups [][]byte
}

// ChatRequestParams holds normalized parameters to serialize a GetChatMessageRequest.
type ChatRequestParams struct {
	ModelUID     string
	SystemPrompt string
	Prompts      []Prompt
	Tools        []Tool
	Temperature  *float64
	TopP         *float64
	MaxTokens    int
	SessionID    string
	CascadeID    string
}

// GenerateSentryTrace produces a Sentry-Trace header value ("<32hex>-<16hex>-1").
func GenerateSentryTrace() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		u1 := strings.ReplaceAll(uuid.New().String(), "-", "")
		u2 := strings.ReplaceAll(uuid.New().String(), "-", "")[:16]
		return u1 + "-" + u2 + "-1"
	}
	return hex.EncodeToString(b[:16]) + "-" + hex.EncodeToString(b[16:24]) + "-1"
}

const maxSessionTurnEntries = 4096

var (
	sessionTurnsMu sync.Mutex
	sessionTurns   = make(map[string]*atomic.Uint64)
)

// NextSessionTurnIndex returns the monotonic 0-based request ordinal for a session (Field 15.2).
func NextSessionTurnIndex(sessionID string) int {
	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		return 0
	}
	sessionTurnsMu.Lock()
	if len(sessionTurns) >= maxSessionTurnEntries {
		clear(sessionTurns)
	}
	counter, ok := sessionTurns[cleanID]
	if !ok {
		counter = &atomic.Uint64{}
		sessionTurns[cleanID] = counter
	}
	sessionTurnsMu.Unlock()
	return int(counter.Add(1) - 1)
}

// WrapConnectEnvelope wraps raw protobuf bytes in a 5-byte Connect-RPC frame envelope.
// Payloads larger than 8 KiB are gzipped automatically to stay well below Devin's 10 MiB body cap.
func WrapConnectEnvelope(protoBytes []byte) ([]byte, bool) {
	if len(protoBytes) > gzipRequestThreshold {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write(protoBytes); err == nil {
			if err := gz.Close(); err == nil && buf.Len() < len(protoBytes) {
				return WrapConnectEnvelopeWithFlag(ConnectFlagCompressed, buf.Bytes()), true
			}
		}
	}
	return WrapConnectEnvelopeWithFlag(ConnectFlagData, protoBytes), false
}

// WrapConnectEnvelopeWithFlag wraps payload bytes with an explicit 5-byte Connect envelope header.
func WrapConnectEnvelopeWithFlag(flag byte, payload []byte) []byte {
	header := make([]byte, 5, 5+len(payload))
	header[0] = flag
	binary.BigEndian.PutUint32(header[1:5], uint32(len(payload)))
	return append(header, payload...)
}

// ReadConnectFrame reads one Connect-RPC stream frame, transparently gunzipping compressed frames.
func ReadConnectFrame(r io.Reader) (flag byte, payload []byte, err error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	flag = header[0]
	if flag != ConnectFlagData &&
		flag != ConnectFlagCompressed &&
		flag != ConnectFlagEndStream &&
		flag != (ConnectFlagCompressed|ConnectFlagEndStream) {
		return flag, nil, fmt.Errorf("invalid connect frame flag: 0x%02x", flag)
	}
	length := binary.BigEndian.Uint32(header[1:5])
	if length > maxConnectFrameSize {
		return flag, nil, fmt.Errorf("connect frame length %d exceeds maximum (%d)", length, maxConnectFrameSize)
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if flag&ConnectFlagCompressed != 0 {
		gz, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return flag, nil, fmt.Errorf("open gzip connect frame: %w", err)
		}
		defer func() { _ = gz.Close() }()

		var decomp bytes.Buffer
		if _, err := decomp.ReadFrom(io.LimitReader(gz, maxDecompressedFrameSize+1)); err != nil {
			return flag, nil, fmt.Errorf("decompress connect frame: %w", err)
		}
		if decomp.Len() > maxDecompressedFrameSize {
			return flag, nil, fmt.Errorf("decompressed connect frame exceeds maximum (%d)", maxDecompressedFrameSize)
		}
		payload = decomp.Bytes()
	}
	return flag, payload, nil
}

// BuildGetChatMessageRequest encodes a GetChatMessageRequest protobuf message.
func BuildGetChatMessageRequest(sessionToken, deviceSeed string, params *ChatRequestParams) []byte {
	maxTokens := params.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	sessionID := strings.TrimSpace(params.SessionID)
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	cascadeID := strings.TrimSpace(params.CascadeID)
	if cascadeID == "" {
		cascadeID = sessionID
	}

	reqBytes := make([]byte, 0, 4096+len(params.SystemPrompt))

	// Field 1: ClientMetadata
	metaBytes := BuildClientMetadataBytes(sessionToken, deviceSeed, runtime.GOOS)
	reqBytes = protowire.AppendTag(reqBytes, 1, protowire.BytesType)
	reqBytes = protowire.AppendBytes(reqBytes, metaBytes)

	// Field 2: System prompt
	if sanitized := SanitizeSystemPrompt(params.SystemPrompt); sanitized != "" {
		reqBytes = protowire.AppendTag(reqBytes, 2, protowire.BytesType)
		reqBytes = protowire.AppendString(reqBytes, sanitized)
	}

	// Field 3: Repeated ChatMessagePrompt
	for _, p := range params.Prompts {
		var pBytes []byte
		msgID := p.MessageID
		if msgID == "" {
			msgID = uuid.New().String()
		}
		pBytes = protowire.AppendTag(pBytes, 1, protowire.BytesType)
		pBytes = protowire.AppendString(pBytes, msgID)

		source := p.Source
		if source <= 0 {
			source = PromptSourceUser
		}
		pBytes = protowire.AppendTag(pBytes, 2, protowire.VarintType)
		pBytes = protowire.AppendVarint(pBytes, uint64(source))

		pBytes = protowire.AppendTag(pBytes, 3, protowire.BytesType)
		pBytes = protowire.AppendString(pBytes, p.Content)

		for _, tc := range p.ToolCalls {
			var tcBytes []byte
			if tc.ID != "" {
				tcBytes = protowire.AppendTag(tcBytes, 1, protowire.BytesType)
				tcBytes = protowire.AppendString(tcBytes, tc.ID)
			}
			if tc.Name != "" {
				tcBytes = protowire.AppendTag(tcBytes, 2, protowire.BytesType)
				tcBytes = protowire.AppendString(tcBytes, tc.Name)
			}
			if tc.Arguments != "" {
				tcBytes = protowire.AppendTag(tcBytes, 3, protowire.BytesType)
				tcBytes = protowire.AppendString(tcBytes, tc.Arguments)
			}
			pBytes = protowire.AppendTag(pBytes, 6, protowire.BytesType)
			pBytes = protowire.AppendBytes(pBytes, tcBytes)
		}

		if p.ToolCallID != "" {
			pBytes = protowire.AppendTag(pBytes, 7, protowire.BytesType)
			pBytes = protowire.AppendString(pBytes, p.ToolCallID)
		}

		// Field 8: prompt_cache_options { type: EPHEMERAL (1) }
		if p.CacheControl {
			var cacheOpt []byte
			cacheOpt = protowire.AppendTag(cacheOpt, 1, protowire.VarintType)
			cacheOpt = protowire.AppendVarint(cacheOpt, 1)
			pBytes = protowire.AppendTag(pBytes, 8, protowire.BytesType)
			pBytes = protowire.AppendBytes(pBytes, cacheOpt)
		}

		for _, img := range p.Images {
			data := strings.TrimSpace(img.Base64Data)
			if data == "" {
				continue
			}
			mime := DetectImageMIME(data, img.MimeType)
			var imgBytes []byte
			imgBytes = protowire.AppendTag(imgBytes, 1, protowire.BytesType)
			imgBytes = protowire.AppendString(imgBytes, data)
			imgBytes = protowire.AppendTag(imgBytes, 2, protowire.BytesType)
			imgBytes = protowire.AppendString(imgBytes, mime)
			pBytes = protowire.AppendTag(pBytes, 10, protowire.BytesType)
			pBytes = protowire.AppendBytes(pBytes, imgBytes)
		}

		if p.Thinking != "" {
			pBytes = protowire.AppendTag(pBytes, 11, protowire.BytesType)
			pBytes = protowire.AppendString(pBytes, p.Thinking)
		}
		if len(p.Signature) > 0 {
			pBytes = protowire.AppendTag(pBytes, 12, protowire.BytesType)
			pBytes = protowire.AppendBytes(pBytes, p.Signature)
		}
		if p.SignatureType != "" {
			pBytes = protowire.AppendTag(pBytes, 18, protowire.BytesType)
			pBytes = protowire.AppendString(pBytes, p.SignatureType)
		}

		reqBytes = protowire.AppendTag(reqBytes, 3, protowire.BytesType)
		reqBytes = protowire.AppendBytes(reqBytes, pBytes)
	}

	// Field 7: fixed flag = 5
	reqBytes = protowire.AppendTag(reqBytes, 7, protowire.VarintType)
	reqBytes = protowire.AppendVarint(reqBytes, 5)

	// Field 8: Completion config.
	// Devin rejects temperature==0 or top_p==0 with "an internal error occurred"; clamp 0 to 1e-6.
	tempVal := 1.0
	if params.Temperature != nil {
		tempVal = *params.Temperature
		if tempVal == 0 {
			tempVal = 1e-6
		}
	}
	topPVal := float64(float32(0.95))
	if params.TopP != nil {
		topPVal = *params.TopP
		if topPVal == 0 {
			topPVal = 1e-6
		}
	}
	var f8Bytes []byte
	f8Bytes = protowire.AppendTag(f8Bytes, 1, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, 1)
	f8Bytes = protowire.AppendTag(f8Bytes, 2, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, uint64(maxTokens))
	f8Bytes = protowire.AppendTag(f8Bytes, 3, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, 400)
	f8Bytes = protowire.AppendTag(f8Bytes, 5, protowire.Fixed64Type)
	f8Bytes = protowire.AppendFixed64(f8Bytes, math.Float64bits(tempVal))
	f8Bytes = protowire.AppendTag(f8Bytes, 7, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, 40)
	f8Bytes = protowire.AppendTag(f8Bytes, 8, protowire.Fixed64Type)
	f8Bytes = protowire.AppendFixed64(f8Bytes, math.Float64bits(topPVal))
	reqBytes = protowire.AppendTag(reqBytes, 8, protowire.BytesType)
	reqBytes = protowire.AppendBytes(reqBytes, f8Bytes)

	// Field 10: Repeated Tools
	for _, tool := range params.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}
		var tBytes []byte
		tBytes = protowire.AppendTag(tBytes, 1, protowire.BytesType)
		tBytes = protowire.AppendString(tBytes, tool.Name)
		desc := strings.TrimSpace(tool.Description)
		if desc == "" {
			desc = tool.Name
		}
		tBytes = protowire.AppendTag(tBytes, 2, protowire.BytesType)
		tBytes = protowire.AppendString(tBytes, desc)
		if len(tool.Parameters) > 0 {
			tBytes = protowire.AppendTag(tBytes, 3, protowire.BytesType)
			tBytes = protowire.AppendBytes(tBytes, tool.Parameters)
		}
		reqBytes = protowire.AppendTag(reqBytes, 10, protowire.BytesType)
		reqBytes = protowire.AppendBytes(reqBytes, tBytes)
	}

	// Field 15: Thread session metadata
	turnIndex := NextSessionTurnIndex(sessionID)
	var f15Bytes []byte
	f15Bytes = protowire.AppendTag(f15Bytes, 1, protowire.BytesType)
	f15Bytes = protowire.AppendString(f15Bytes, sessionID)
	if turnIndex > 0 {
		f15Bytes = protowire.AppendTag(f15Bytes, 2, protowire.VarintType)
		f15Bytes = protowire.AppendVarint(f15Bytes, uint64(turnIndex))
	}
	f15Bytes = protowire.AppendTag(f15Bytes, 3, protowire.VarintType)
	f15Bytes = protowire.AppendVarint(f15Bytes, 4)
	if len(params.Prompts) > 0 && params.Prompts[len(params.Prompts)-1].Source == PromptSourceUser {
		if turnIndex == 0 || len(params.Prompts) < 2 || params.Prompts[len(params.Prompts)-2].Source != PromptSourceUser {
			f15Bytes = protowire.AppendTag(f15Bytes, 4, protowire.VarintType)
			f15Bytes = protowire.AppendVarint(f15Bytes, 14)
		}
	}
	reqBytes = protowire.AppendTag(reqBytes, 15, protowire.BytesType)
	reqBytes = protowire.AppendBytes(reqBytes, f15Bytes)

	// Field 16: Cascade ID (session-stable prompt cache key)
	reqBytes = protowire.AppendTag(reqBytes, 16, protowire.BytesType)
	reqBytes = protowire.AppendString(reqBytes, cascadeID)

	// Field 20: fixed flag = 1
	reqBytes = protowire.AppendTag(reqBytes, 20, protowire.VarintType)
	reqBytes = protowire.AppendVarint(reqBytes, 1)

	// Field 21: Model UID
	reqBytes = protowire.AppendTag(reqBytes, 21, protowire.BytesType)
	reqBytes = protowire.AppendString(reqBytes, params.ModelUID)

	return reqBytes
}

// NewChatHTTPRequest constructs the Connect-RPC HTTP request for GetChatMessage.
func NewChatHTTPRequest(ctx context.Context, baseURL, sessionToken string, protoBytes []byte) (*http.Request, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = DefaultServerURL
	}
	framed, compressed := WrapConnectEnvelope(protoBytes)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+GetChatMessagePath, bytes.NewReader(framed))
	if err != nil {
		return nil, err
	}
	token := FormatSessionToken(sessionToken)
	req.Header.Set("Authorization", fmt.Sprintf("Basic %s-%s", token, token))
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Sentry-Trace", GenerateSentryTrace())
	if compressed {
		req.Header.Set("Connect-Content-Encoding", "gzip")
	}
	req.Header["User-Agent"] = []string{""}
	return req, nil
}

// ParseFrame decodes a single GetChatMessageResponse protobuf frame.
func ParseFrame(payload []byte) (FrameResult, error) {
	var res FrameResult
	var textParts []string
	var thinkingParts []string

	pos := 0
	for pos < len(payload) {
		num, typ, n := protowire.ConsumeTag(payload[pos:])
		if n <= 0 {
			return res, fmt.Errorf("consume tag at offset %d: %w", pos, protowire.ParseError(n))
		}
		pos += n

		switch typ {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(payload[pos:])
			if vn <= 0 {
				return res, fmt.Errorf("consume varint at offset %d: %w", pos, protowire.ParseError(vn))
			}
			pos += vn
			switch num {
			case 2:
				res.Timestamp = v
			case 4:
				res.DeltaTokens = v
			case 5:
				res.StopReason = v
			}
		case protowire.Fixed64Type:
			_, fn := protowire.ConsumeFixed64(payload[pos:])
			if fn <= 0 {
				return res, fmt.Errorf("consume fixed64 at offset %d: %w", pos, protowire.ParseError(fn))
			}
			pos += fn
		case protowire.Fixed32Type:
			_, fn := protowire.ConsumeFixed32(payload[pos:])
			if fn <= 0 {
				return res, fmt.Errorf("consume fixed32 at offset %d: %w", pos, protowire.ParseError(fn))
			}
			pos += fn
		case protowire.BytesType:
			val, bn := protowire.ConsumeBytes(payload[pos:])
			if bn <= 0 {
				return res, fmt.Errorf("consume bytes at offset %d: %w", pos, protowire.ParseError(bn))
			}
			pos += bn
			switch num {
			case 1:
				res.OutputID = string(val)
			case 2:
				res.Timestamp = uint64(parseTimestampSeconds(val))
			case 3:
				textParts = append(textParts, string(val))
			case 6:
				if tc, err := parseToolCallDelta(val); err == nil {
					res.ToolCallDeltas = append(res.ToolCallDeltas, tc)
				}
			case 7:
				res.Usage = parseUsageField(val)
			case 9:
				thinkingParts = append(thinkingParts, string(val))
			case 10:
				res.DeltaSignature = append(res.DeltaSignature, val...)
			case 17:
				res.MessageID = string(val)
			case 21:
				res.DeltaSignatureType = string(val)
			case 28:
				copied := append([]byte(nil), val...)
				res.ResponseDimensionGroups = append(res.ResponseDimensionGroups, copied)
			}
		default:
			return res, fmt.Errorf("unsupported wire type %d at offset %d", typ, pos)
		}
	}

	if len(textParts) > 0 {
		res.ContentText = strings.Join(textParts, "")
	}
	if len(thinkingParts) > 0 {
		res.ThinkingText = strings.Join(thinkingParts, "")
	}
	return res, nil
}

func parseToolCallDelta(data []byte) (ToolCallDelta, error) {
	var tc ToolCallDelta
	pos := 0
	for pos < len(data) {
		num, typ, n := protowire.ConsumeTag(data[pos:])
		if n <= 0 {
			return tc, protowire.ParseError(n)
		}
		pos += n
		switch typ {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(data[pos:])
			if vn <= 0 {
				return tc, protowire.ParseError(vn)
			}
			pos += vn
			if num == 6 {
				tc.IsCustomToolCall = v != 0
			}
		case protowire.BytesType:
			val, bn := protowire.ConsumeBytes(data[pos:])
			if bn <= 0 {
				return tc, protowire.ParseError(bn)
			}
			pos += bn
			switch num {
			case 1:
				tc.ID = string(val)
			case 2:
				tc.Name = string(val)
			case 3:
				tc.Arguments = string(val)
			case 4:
				tc.InvalidJSONStr = string(val)
			case 5:
				tc.InvalidJSONErr = string(val)
			}
		default:
			skip := protowire.ConsumeFieldValue(num, typ, data[pos:])
			if skip <= 0 {
				return tc, protowire.ParseError(skip)
			}
			pos += skip
		}
	}
	return tc, nil
}

func parseUsageField(data []byte) *Usage {
	u := &Usage{}
	pos := 0
	for pos < len(data) {
		num, typ, n := protowire.ConsumeTag(data[pos:])
		if n <= 0 {
			break
		}
		pos += n
		switch typ {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(data[pos:])
			if vn <= 0 {
				return u
			}
			pos += vn
			switch num {
			case 2:
				u.PromptTokens += int64(v)
			case 3:
				u.CompletionTokens = int64(v)
			case 4:
				u.CacheWriteTokens += int64(v)
			case 5:
				u.CachedTokens = int64(v)
			case 6:
				u.StatusCode = v
			}
		case protowire.BytesType:
			val, bn := protowire.ConsumeBytes(data[pos:])
			if bn <= 0 {
				return u
			}
			pos += bn
			switch num {
			case 8:
				k, v := parseHeaderPair(val)
				if (strings.EqualFold(k, "x-request-id") || strings.EqualFold(k, "request-id")) && v != "" {
					u.RequestID = v
				} else if k == "" && len(val) > 0 && isPrintableASCII(val) && u.RequestID == "" {
					u.RequestID = string(val)
				}
			case 9:
				u.ModelName = string(val)
			}
		default:
			skip := protowire.ConsumeFieldValue(num, typ, data[pos:])
			if skip <= 0 {
				return u
			}
			pos += skip
		}
	}
	return u
}

func parseHeaderPair(data []byte) (string, string) {
	var k, v string
	pos := 0
	for pos < len(data) {
		num, typ, n := protowire.ConsumeTag(data[pos:])
		if n <= 0 {
			break
		}
		pos += n
		if typ == protowire.BytesType {
			b, bn := protowire.ConsumeBytes(data[pos:])
			if bn <= 0 {
				return k, v
			}
			pos += bn
			if num == 1 {
				k = string(b)
			} else if num == 2 {
				v = string(b)
			}
		} else {
			skip := protowire.ConsumeFieldValue(num, typ, data[pos:])
			if skip <= 0 {
				return k, v
			}
			pos += skip
		}
	}
	return k, v
}

func isPrintableASCII(b []byte) bool {
	for _, c := range b {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}

// ParseResponseDimensionGroups extracts fallback Token Usage metrics from Field 28.
func ParseResponseDimensionGroups(groups ...[]byte) (promptTokens, completionTokens, cachedTokens int64, found bool) {
	for _, gBytes := range groups {
		if len(gBytes) == 0 {
			continue
		}
		gPos := 0
		var title string
		type metricItem struct {
			key string
			val float32
		}
		var metrics []metricItem
		for gPos < len(gBytes) {
			gNum, gTyp, gn := protowire.ConsumeTag(gBytes[gPos:])
			if gn <= 0 {
				break
			}
			gPos += gn
			if gTyp != protowire.BytesType {
				skip := protowire.ConsumeFieldValue(gNum, gTyp, gBytes[gPos:])
				if skip <= 0 {
					break
				}
				gPos += skip
				continue
			}
			gb, gbn := protowire.ConsumeBytes(gBytes[gPos:])
			if gbn <= 0 {
				break
			}
			gPos += gbn
			if gNum == 1 {
				title = string(gb)
			} else if gNum == 2 {
				mPos := 0
				var mKey string
				var mVal float32
				for mPos < len(gb) {
					mNum, mTyp, mn := protowire.ConsumeTag(gb[mPos:])
					if mn <= 0 {
						break
					}
					mPos += mn
					if mTyp != protowire.BytesType {
						skip := protowire.ConsumeFieldValue(mNum, mTyp, gb[mPos:])
						if skip <= 0 {
							break
						}
						mPos += skip
						continue
					}
					mb, mbn := protowire.ConsumeBytes(gb[mPos:])
					if mbn <= 0 {
						break
					}
					mPos += mbn
					if mNum == 5 {
						mKey = string(mb)
					} else if mNum == 4 {
						dPos := 0
						for dPos < len(mb) {
							dNum, dTyp, dn := protowire.ConsumeTag(mb[dPos:])
							if dn <= 0 {
								break
							}
							dPos += dn
							if dTyp == protowire.Fixed32Type {
								dv, dfn := protowire.ConsumeFixed32(mb[dPos:])
								if dfn <= 0 {
									break
								}
								dPos += dfn
								if dNum == 2 {
									mVal = math.Float32frombits(dv)
								}
							} else {
								skip := protowire.ConsumeFieldValue(dNum, dTyp, mb[dPos:])
								if skip <= 0 {
									break
								}
								dPos += skip
							}
						}
					}
				}
				if mKey != "" {
					metrics = append(metrics, metricItem{key: mKey, val: mVal})
				}
			}
		}
		if strings.EqualFold(title, "Token Usage") {
			for _, m := range metrics {
				switch m.key {
				case "input_tokens":
					promptTokens = int64(m.val)
					found = true
				case "output_tokens":
					completionTokens = int64(m.val)
					found = true
				case "cached_input_tokens":
					cachedTokens = int64(m.val)
					found = true
				}
			}
			if found {
				return promptTokens, completionTokens, cachedTokens, true
			}
		}
	}
	return 0, 0, 0, false
}

// ParseTrailerError parses a Connect-RPC EOS trailer JSON frame and maps it to an HTTP status code.
func ParseTrailerError(payload []byte) (httpStatus int, code string, message string, err error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return 0, "", "", nil
	}
	var trailer struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if unmarshalErr := json.Unmarshal(trimmed, &trailer); unmarshalErr != nil || trailer.Error == nil {
		return 0, "", "", nil
	}
	code = strings.ToLower(strings.TrimSpace(trailer.Error.Code))
	message = strings.TrimSpace(trailer.Error.Message)
	msgLower := strings.ToLower(message)

	httpStatus = http.StatusBadGateway
	switch code {
	case "invalid_argument":
		if strings.Contains(msgLower, "internal error") {
			httpStatus = http.StatusBadGateway
		} else {
			httpStatus = http.StatusBadRequest
		}
	case "internal":
		httpStatus = http.StatusBadGateway
	case "unauthenticated":
		httpStatus = http.StatusUnauthorized
	case "permission_denied":
		if strings.Contains(msgLower, "high demand") {
			httpStatus = http.StatusTooManyRequests
		} else {
			httpStatus = http.StatusForbidden
		}
	case "resource_exhausted":
		httpStatus = http.StatusTooManyRequests
	case "unavailable":
		httpStatus = http.StatusServiceUnavailable
	case "canceled":
		httpStatus = 499
	case "deadline_exceeded":
		httpStatus = http.StatusGatewayTimeout
	case "failed_precondition":
		if strings.Contains(msgLower, "quota") ||
			strings.Contains(msgLower, "credit") ||
			strings.Contains(msgLower, "acu") ||
			strings.Contains(msgLower, "exhausted") ||
			strings.Contains(msgLower, "limit") {
			httpStatus = http.StatusTooManyRequests
		} else {
			httpStatus = http.StatusBadRequest
		}
	}
	return httpStatus, code, message, fmt.Errorf("devin upstream error (%s): %s", trailer.Error.Code, message)
}

// QuotaExhaustionPeriod detects explicit account-wide daily/weekly quota exhaustion errors.
func QuotaExhaustionPeriod(message string) QuotaPeriod {
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "quota") || !strings.Contains(lower, "exhausted") {
		return QuotaPeriodNone
	}
	if strings.Contains(lower, "weekly") {
		return QuotaPeriodWeekly
	}
	if strings.Contains(lower, "daily") {
		return QuotaPeriodDaily
	}
	return QuotaPeriodNone
}

// IsRequestIssueError reports Devin's generic invalid_argument rejection often triggered
// by incompatible cross-provider thinking signatures.
func IsRequestIssueError(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "issue with this request") || strings.Contains(lower, "invalid signature")
}

// SanitizeSystemPrompt strips client-specific attribution/billing lines that cause Devin rejections
// or break prompt cache stability.
func SanitizeSystemPrompt(prompt string) string {
	if prompt == "" {
		return ""
	}
	normalized := strings.ReplaceAll(prompt, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "x-anthropic-billing-header:") {
			continue
		}
		if strings.HasPrefix(trimmed, "You are Claude Code") {
			continue
		}
		if strings.Contains(trimmed, "authorized security testing") || strings.Contains(trimmed, "destructive techniques, DoS attacks") {
			continue
		}
		if strings.Contains(trimmed, "Claude Code is available as a CLI") {
			continue
		}
		if strings.Contains(trimmed, "Fast mode for Claude Code") {
			continue
		}
		if strings.Contains(trimmed, "Codex refers to the open-source agentic coding interface") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// DetectImageMIME sniffs the real MIME type from base64 magic bytes, falling back to declared.
// Devin rejects mismatches such as a PNG labelled image/jpeg on Claude models.
func DetectImageMIME(base64Data, declared string) string {
	sample := strings.TrimSpace(base64Data)
	if len(sample) > 32 {
		sample = sample[:32]
	}
	// Pad base64 slice if needed
	if rem := len(sample) % 4; rem != 0 {
		sample = sample[:len(sample)-rem]
	}
	if decoded, err := base64.StdEncoding.DecodeString(sample); err == nil && len(decoded) >= 4 {
		switch {
		case len(decoded) >= 8 && decoded[0] == 0x89 && decoded[1] == 0x50 && decoded[2] == 0x4e && decoded[3] == 0x47:
			return "image/png"
		case decoded[0] == 0xff && decoded[1] == 0xd8 && decoded[2] == 0xff:
			return "image/jpeg"
		case len(decoded) >= 6 && string(decoded[:4]) == "GIF8":
			return "image/gif"
		case len(decoded) >= 12 && string(decoded[:4]) == "RIFF" && string(decoded[8:12]) == "WEBP":
			return "image/webp"
		}
	}
	mime := strings.TrimSpace(declared)
	if mime == "" {
		return "image/png"
	}
	return mime
}

func isSafeToolCallID(id string) bool {
	if id == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// EncodeToolCallID ensures downstream tool_use IDs match Anthropic's strict [A-Za-z0-9_-]+ regex.
func EncodeToolCallID(id string) string {
	if id == "" {
		return "toolu_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	}
	if isSafeToolCallID(id) && !strings.HasPrefix(id, "dv_") {
		return id
	}
	return "dv_" + base64.RawURLEncoding.EncodeToString([]byte(id))
}

// DecodeToolCallID reverses EncodeToolCallID before sending tool results back to Devin.
func DecodeToolCallID(id string) string {
	if !strings.HasPrefix(id, "dv_") || !isSafeToolCallID(id) {
		return id
	}
	raw, err := base64.RawURLEncoding.DecodeString(id[3:])
	if err != nil || len(raw) == 0 {
		return id
	}
	if EncodeToolCallID(string(raw)) != id {
		return id
	}
	return string(raw)
}

// UTF8SplitBuffer buffers incomplete trailing UTF-8 byte sequences across chunk boundaries.
type UTF8SplitBuffer struct {
	remainder []byte
}

// Feed appends chunk and returns all complete UTF-8 runes.
func (b *UTF8SplitBuffer) Feed(chunk []byte) string {
	combined := append(b.remainder, chunk...)
	b.remainder = nil
	if len(combined) == 0 {
		return ""
	}
	validUntil := 0
	for validUntil < len(combined) {
		r, size := utf8.DecodeRune(combined[validUntil:])
		if r == utf8.RuneError && size == 1 {
			trailing := len(combined) - validUntil
			if trailing < utf8.UTFMax && !utf8.FullRune(combined[validUntil:]) {
				break
			}
			validUntil++
			continue
		}
		validUntil += size
	}
	out := string(combined[:validUntil])
	b.remainder = append(b.remainder, combined[validUntil:]...)
	return out
}
