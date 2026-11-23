package kimi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDomainNormalizationAndResolution(t *testing.T) {
	if got := NormalizeDomain(""); got != DefaultDomain {
		t.Fatalf("expected %s, got %s", DefaultDomain, got)
	}
	if got := NormalizeDomain("kimi.ai"); got != AIDomain {
		t.Fatalf("expected %s, got %s", AIDomain, got)
	}
	if got := ResolveOAuthHost("kimi-ai"); got != OAuthHostAI {
		t.Fatalf("expected %s, got %s", OAuthHostAI, got)
	}
	if got := ResolveAPIBaseURL("kimi.com"); got != APIBaseURLCom {
		t.Fatalf("expected %s, got %s", APIBaseURLCom, got)
	}
	if got := ResolveAPIBaseURL("kimi.ai"); got != APIBaseURLAI {
		t.Fatalf("expected %s, got %s", APIBaseURLAI, got)
	}
}

func TestDeviceFlowAndRefresh(t *testing.T) {
	pollCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Msh-Device-Id") != "dev-123" {
			t.Errorf("missing X-Msh-Device-Id header, got %q", r.Header.Get("X-Msh-Device-Id"))
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		switch r.URL.Path {
		case DeviceAuthorizationPath:
			if r.FormValue("client_id") != ClientID {
				t.Errorf("unexpected client_id %q", r.FormValue("client_id"))
			}
			_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
				UserCode:                "ABCD-1234",
				DeviceCode:              "dc-xyz",
				VerificationURI:         "https://kimi.com/activate",
				VerificationURIComplete: "https://kimi.com/activate?user_code=ABCD-1234",
				ExpiresIn:               600,
				Interval:                5,
			})
		case TokenPath:
			if r.FormValue("grant_type") == "refresh_token" {
				if r.FormValue("refresh_token") != "rt-1" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "at-2",
					"refresh_token": "rt-2",
					"token_type":    "Bearer",
					"expires_in":    3600,
				})
				return
			}
			pollCount++
			if pollCount == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "at-1",
				"refresh_token": "rt-1",
				"token_type":    "Bearer",
				"expires_in":    1800,
				"scope":         "kimi-code",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewClient()
	client.SetOAuthHostOverride(srv.URL)

	dcr, err := client.RequestDeviceCode(context.Background(), DefaultDomain, "dev-123", "")
	if err != nil {
		t.Fatalf("RequestDeviceCode failed: %v", err)
	}
	if dcr.UserCode != "ABCD-1234" || dcr.DeviceCode != "dc-xyz" {
		t.Fatalf("unexpected device code response: %+v", dcr)
	}

	res1, err := client.PollDeviceTokenOnce(context.Background(), DefaultDomain, dcr.DeviceCode, "dev-123", "")
	if err != nil || res1.Status != PollStatusPending {
		t.Fatalf("expected pending, got %+v err=%v", res1, err)
	}

	res2, err := client.PollDeviceTokenOnce(context.Background(), DefaultDomain, dcr.DeviceCode, "dev-123", "")
	if err != nil || res2.Status != PollStatusAuthorized || res2.Token == nil || res2.Token.AccessToken != "at-1" {
		t.Fatalf("expected authorized with at-1, got %+v err=%v", res2, err)
	}

	refreshed, err := client.RefreshToken(context.Background(), DefaultDomain, res2.Token.RefreshToken, "dev-123", "")
	if err != nil || refreshed.AccessToken != "at-2" || refreshed.RefreshToken != "rt-2" {
		t.Fatalf("expected refreshed at-2/rt-2, got %+v err=%v", refreshed, err)
	}
}
