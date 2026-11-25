package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type PricingServiceSuite struct {
	suite.Suite
	ctx    context.Context
	srv    *httptest.Server
	client *pricingRemoteClient
}

func (s *PricingServiceSuite) SetupTest() {
	s.ctx = context.Background()
	client, ok := NewPricingRemoteClient("", false).(*pricingRemoteClient)
	require.True(s.T(), ok, "type assertion failed")
	s.client = client
}

func (s *PricingServiceSuite) TearDownTest() {
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
}

func (s *PricingServiceSuite) setupServer(handler http.HandlerFunc) {
	s.srv = newLocalTestServer(s.T(), handler)
}

func (s *PricingServiceSuite) TestFetchPricingJSON_Success() {
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"anthropic":{"id":"anthropic","name":"Anthropic","models":{}}}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))

	body, err := s.client.FetchPricingJSON(s.ctx, s.srv.URL+"/ok")
	require.NoError(s.T(), err, "FetchPricingJSON")
	require.Contains(s.T(), string(body), `"anthropic"`)
}

func (s *PricingServiceSuite) TestFetchPricingJSON_NonOKStatus() {
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	_, err := s.client.FetchPricingJSON(s.ctx, s.srv.URL+"/err")
	require.Error(s.T(), err, "expected error for non-200 status")
}

func (s *PricingServiceSuite) TestFetchPricingJSON_InvalidURL() {
	_, err := s.client.FetchPricingJSON(s.ctx, "://invalid-url")
	require.Error(s.T(), err, "expected error for invalid URL")
}

func (s *PricingServiceSuite) TestFetchPricingJSON_ContextCancel() {
	started := make(chan struct{})
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))

	ctx, cancel := context.WithCancel(s.ctx)

	done := make(chan error, 1)
	go func() {
		_, err := s.client.FetchPricingJSON(ctx, s.srv.URL+"/block")
		done <- err
	}()

	<-started
	cancel()

	err := <-done
	require.Error(s.T(), err)
}

func TestNewPricingRemoteClient_InvalidProxy_NoFallback(t *testing.T) {
	client := NewPricingRemoteClient("://bad", false)
	_, ok := client.(*pricingRemoteClientError)
	require.True(t, ok, "should return error client when proxy is invalid and fallback disabled")

	_, err := client.FetchPricingJSON(context.Background(), "https://models.dev/api.json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "proxy client init failed")
}

func TestNewPricingRemoteClient_InvalidProxy_WithFallback(t *testing.T) {
	client := NewPricingRemoteClient("://bad", true)
	_, ok := client.(*pricingRemoteClient)
	require.True(t, ok, "should fallback to direct client when allowed")
}

func TestPricingServiceSuite(t *testing.T) {
	suite.Run(t, new(PricingServiceSuite))
}
