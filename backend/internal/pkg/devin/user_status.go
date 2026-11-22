package devin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// GetUserStatusPath is the Connect-RPC unary endpoint for seat management and quota.
	GetUserStatusPath = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"

	// DefaultClientName matches the Devin CLI client identifier in protobuf metadata.
	DefaultClientName = "chisel"
	// DefaultClientVersion matches the Devin CLI client version in protobuf metadata.
	DefaultClientVersion = "3000.11.3"
	// DeviceFingerprintHexLen is the 732-character hex device fingerprint length expected by Devin.
	DeviceFingerprintHexLen = 732
)

// UserStatusError carries HTTP status metadata from GetUserStatus failures.
type UserStatusError struct {
	StatusCode int
	Body       string
}

func (e *UserStatusError) Error() string {
	return fmt.Sprintf("devin GetUserStatus error (HTTP %d): %s", e.StatusCode, e.Body)
}

// UserStatus holds account metadata, plan info, and daily/weekly quota windows.
type UserStatus struct {
	Email                       string    `json:"email,omitempty"`
	UserName                    string    `json:"user_name,omitempty"`
	UserID                      string    `json:"user_id,omitempty"`
	TeamID                      string    `json:"team_id,omitempty"`
	OrgID                       string    `json:"org_id,omitempty"`
	OrgName                     string    `json:"org_name,omitempty"`
	Plan                        string    `json:"plan,omitempty"`
	DailyQuotaRemainingPercent  int64     `json:"daily_quota_remaining_percent"`
	WeeklyQuotaRemainingPercent int64     `json:"weekly_quota_remaining_percent"`
	DailyQuotaResetAt           time.Time `json:"daily_quota_reset_at,omitempty"`
	WeeklyQuotaResetAt          time.Time `json:"weekly_quota_reset_at,omitempty"`
	PlanStart                   time.Time `json:"plan_start,omitempty"`
	PlanEnd                     time.Time `json:"plan_end,omitempty"`
}

// GenerateDeviceFingerprint produces a 732-character hex device fingerprint.
// When seed is empty, a random fingerprint is generated; otherwise it is derived deterministically.
func GenerateDeviceFingerprint(seed string) string {
	if seed == "" {
		var b [DeviceFingerprintHexLen / 2]byte
		if _, err := rand.Read(b[:]); err == nil {
			return hex.EncodeToString(b[:])
		}
		seed = uuid.New().String()
	}
	var sb strings.Builder
	counter := 0
	for sb.Len() < DeviceFingerprintHexLen {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", seed, counter)))
		sb.WriteString(hex.EncodeToString(h[:]))
		counter++
	}
	return sb.String()[:DeviceFingerprintHexLen]
}

// BuildClientMetadataBytes serializes the ClientMetadata protobuf (Field 1).
func BuildClientMetadataBytes(sessionToken, deviceSeed, osName string) []byte {
	if osName == "" {
		osName = runtime.GOOS
	}
	fp := GenerateDeviceFingerprint(deviceSeed)

	var out []byte
	out = protowire.AppendTag(out, 1, protowire.BytesType)
	out = protowire.AppendString(out, DefaultClientName)

	out = protowire.AppendTag(out, 2, protowire.BytesType)
	out = protowire.AppendString(out, DefaultClientVersion)

	out = protowire.AppendTag(out, 3, protowire.BytesType)
	out = protowire.AppendString(out, sessionToken)

	out = protowire.AppendTag(out, 4, protowire.BytesType)
	out = protowire.AppendString(out, "en")

	out = protowire.AppendTag(out, 5, protowire.BytesType)
	out = protowire.AppendString(out, osName)

	out = protowire.AppendTag(out, 7, protowire.BytesType)
	out = protowire.AppendString(out, DefaultClientVersion)

	out = protowire.AppendTag(out, 12, protowire.BytesType)
	out = protowire.AppendString(out, DefaultClientName)

	out = protowire.AppendTag(out, 28, protowire.BytesType)
	out = protowire.AppendString(out, DefaultClientName)

	out = protowire.AppendTag(out, 31, protowire.BytesType)
	out = protowire.AppendString(out, fp)
	return out
}

// BuildGetUserStatusRequest encodes a unary Connect-RPC GetUserStatus request protobuf.
func BuildGetUserStatusRequest(sessionToken, deviceSeed string) []byte {
	if deviceSeed == "" {
		deviceSeed = sessionToken
	}
	meta := BuildClientMetadataBytes(sessionToken, deviceSeed, runtime.GOOS)
	var req []byte
	req = protowire.AppendTag(req, 1, protowire.BytesType)
	req = protowire.AppendBytes(req, meta)
	return req
}

// ParseGetUserStatusResponse decodes the binary protobuf returned by GetUserStatus.
func ParseGetUserStatusResponse(data []byte) (*UserStatus, error) {
	if len(data) == 0 {
		return nil, errors.New("empty GetUserStatus response")
	}

	status := &UserStatus{}
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n <= 0 {
			return nil, protowire.ParseError(n)
		}
		rem = rem[n:]

		if num == 1 && wireType == protowire.BytesType {
			userStatusBytes, m := protowire.ConsumeBytes(rem)
			if m <= 0 {
				return nil, protowire.ParseError(m)
			}
			rem = rem[m:]
			parseUserStatusFields(userStatusBytes, status)
		} else {
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m <= 0 {
				return nil, protowire.ParseError(m)
			}
			rem = rem[m:]
		}
	}
	return status, nil
}

func parseUserStatusFields(data []byte, status *UserStatus) {
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n <= 0 {
			return
		}
		rem = rem[n:]
		if wireType == protowire.BytesType {
			val, m := protowire.ConsumeBytes(rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
			switch num {
			case 3:
				status.UserName = string(val)
			case 5:
				status.TeamID = string(val)
			case 7:
				status.Email = string(val)
			case 13:
				parsePlanStatusFields(val, status)
			case 36:
				status.UserID = string(val)
			}
		} else {
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
		}
	}
}

func parsePlanStatusFields(data []byte, status *UserStatus) {
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n <= 0 {
			return
		}
		rem = rem[n:]
		switch wireType {
		case protowire.BytesType:
			val, m := protowire.ConsumeBytes(rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
			switch num {
			case 1:
				parsePlanInfoFields(val, status)
			case 2:
				if sec := parseTimestampSeconds(val); sec > 0 {
					status.PlanStart = time.Unix(sec, 0).UTC()
				}
			case 3:
				if sec := parseTimestampSeconds(val); sec > 0 {
					status.PlanEnd = time.Unix(sec, 0).UTC()
				}
			}
		case protowire.VarintType:
			val, m := protowire.ConsumeVarint(rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
			switch num {
			case 14:
				status.DailyQuotaRemainingPercent = int64(val)
			case 15:
				status.WeeklyQuotaRemainingPercent = int64(val)
			case 17:
				if val > 0 {
					status.DailyQuotaResetAt = time.Unix(int64(val), 0).UTC()
				}
			case 18:
				if val > 0 {
					status.WeeklyQuotaResetAt = time.Unix(int64(val), 0).UTC()
				}
			}
		default:
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
		}
	}
}

func parsePlanInfoFields(data []byte, status *UserStatus) {
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n <= 0 {
			return
		}
		rem = rem[n:]
		if wireType == protowire.BytesType {
			val, m := protowire.ConsumeBytes(rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
			switch num {
			case 2:
				status.Plan = string(val)
			case 33:
				parsePlanOrgFields(val, status)
			}
		} else {
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
		}
	}
}

func parsePlanOrgFields(data []byte, status *UserStatus) {
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n <= 0 {
			return
		}
		rem = rem[n:]
		if wireType == protowire.BytesType {
			val, m := protowire.ConsumeBytes(rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
			switch num {
			case 4:
				status.OrgID = string(val)
			case 8:
				status.OrgName = string(val)
			}
		} else {
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
		}
	}
}

func parseTimestampSeconds(data []byte) int64 {
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n <= 0 {
			return 0
		}
		rem = rem[n:]
		if wireType == protowire.VarintType {
			val, m := protowire.ConsumeVarint(rem)
			if m <= 0 {
				return 0
			}
			rem = rem[m:]
			if num == 1 {
				return int64(val)
			}
		} else {
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m <= 0 {
				return 0
			}
			rem = rem[m:]
		}
	}
	return 0
}

// FetchUserStatus calls the unary Connect-RPC GetUserStatus endpoint.
func (c *Client) FetchUserStatus(ctx context.Context, sessionToken, deviceSeed string) (*UserStatus, error) {
	if c == nil || c.httpClient == nil {
		return nil, errors.New("devin client is not initialized")
	}
	token := FormatSessionToken(sessionToken)
	if token == "" {
		return nil, errors.New("devin session token is required")
	}

	reqBody := BuildGetUserStatusRequest(token, deviceSeed)
	endpoint := strings.TrimRight(c.ServerBaseURL(), "/") + GetUserStatusPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Basic %s-%s", token, token))
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Accept", "*/*")
	req.Header["User-Agent"] = []string{""}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &UserStatusError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(respBytes)),
		}
	}
	return ParseGetUserStatusResponse(respBytes)
}
