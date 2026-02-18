package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestFacebookPageConnectorPublishSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST method, got %s", r.Method)
		}

		if r.URL.Path != "/v21.0/page_123/feed" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}

		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}

		if values.Get("message") != "Hello from tests" {
			t.Fatalf("unexpected message: %s", values.Get("message"))
		}
		if values.Get("access_token") != "page-token" {
			t.Fatalf("unexpected access token")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"page_123_456"}`))
	}))
	defer srv.Close()

	connector := newFacebookPageConnectorWithClient(srv.URL, srv.Client())

	result, err := connector.Publish(context.Background(), Account{
		Provider:   "facebook_page",
		ProviderID: "page_123",
		Data: map[string]any{
			"api_version": "v21.0",
		},
		AccessToken: map[string]any{
			"page_access_token": "page-token",
		},
	}, PublishRequest{Text: "Hello from tests"})
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	if result.ProviderPostID != "page_123_456" {
		t.Fatalf("unexpected provider post id: %s", result.ProviderPostID)
	}
	if result.Data["provider"] != "facebook_page" {
		t.Fatalf("unexpected provider metadata: %v", result.Data)
	}
}

func TestFacebookPageConnectorPublishUnauthorized(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid OAuth access token.","code":190}}`))
	}))
	defer srv.Close()

	connector := newFacebookPageConnectorWithClient(srv.URL, srv.Client())

	_, err := connector.Publish(context.Background(), Account{
		Provider:   "facebook_page",
		ProviderID: "page_123",
		AccessToken: map[string]any{
			"page_access_token": "bad-token",
		},
	}, PublishRequest{Text: "hello"})
	if err == nil {
		t.Fatalf("expected unauthorized error")
	}
	if err != ErrUnauthorized {
		t.Fatalf("expected unauthorized error, got: %v", err)
	}
}

func TestFacebookPageConnectorPublishInvalidContent(t *testing.T) {
	t.Parallel()

	connector := newFacebookPageConnector()

	_, err := connector.Publish(context.Background(), Account{
		Provider:   "facebook_page",
		ProviderID: "page_123",
		AccessToken: map[string]any{
			"page_access_token": "token",
		},
	}, PublishRequest{Text: "   "})
	if err == nil {
		t.Fatalf("expected invalid content error")
	}
	if err != ErrInvalidContent {
		t.Fatalf("expected ErrInvalidContent, got %v", err)
	}
}
