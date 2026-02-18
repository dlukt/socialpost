package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFacebookGraphClientExchangeUserAccessTokenSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v21.0/oauth/access_token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("grant_type") != "fb_exchange_token" {
			t.Fatalf("unexpected grant_type")
		}
		if query.Get("client_id") != "app-id" {
			t.Fatalf("unexpected client_id")
		}
		if query.Get("client_secret") != "app-secret" {
			t.Fatalf("unexpected client_secret")
		}
		if query.Get("fb_exchange_token") != "short-token" {
			t.Fatalf("unexpected fb_exchange_token")
		}
		_, _ = w.Write([]byte(`{"access_token":"long-token","token_type":"bearer"}`))
	}))
	defer srv.Close()

	client := newFacebookGraphClientWithClient(srv.URL, srv.Client())
	result, err := client.ExchangeUserAccessToken(context.Background(), "v21.0", "app-id", "app-secret", "short-token")
	if err != nil {
		t.Fatalf("exchange failed: %v", err)
	}
	if result != "long-token" {
		t.Fatalf("unexpected token: %s", result)
	}
}

func TestFacebookGraphClientExchangeAuthorizationCodeSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v21.0/oauth/access_token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("client_id") != "app-id" {
			t.Fatalf("unexpected client_id")
		}
		if query.Get("client_secret") != "app-secret" {
			t.Fatalf("unexpected client_secret")
		}
		if query.Get("redirect_uri") != "https://example.com/callback" {
			t.Fatalf("unexpected redirect_uri")
		}
		if query.Get("code") != "auth-code" {
			t.Fatalf("unexpected code")
		}
		_, _ = w.Write([]byte(`{"access_token":"short-lived-token"}`))
	}))
	defer srv.Close()

	client := newFacebookGraphClientWithClient(srv.URL, srv.Client())
	result, err := client.ExchangeAuthorizationCode(context.Background(), "v21.0", "app-id", "app-secret", "https://example.com/callback", "auth-code")
	if err != nil {
		t.Fatalf("exchange failed: %v", err)
	}
	if result != "short-lived-token" {
		t.Fatalf("unexpected token: %s", result)
	}
}

func TestFacebookGraphClientExchangeAuthorizationCodeUnauthorized(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid OAuth code.","code":190}}`))
	}))
	defer srv.Close()

	client := newFacebookGraphClientWithClient(srv.URL, srv.Client())
	_, err := client.ExchangeAuthorizationCode(context.Background(), "v21.0", "app-id", "app-secret", "https://example.com/callback", "bad-code")
	if err == nil {
		t.Fatalf("expected error")
	}
	if err != ErrUnauthorized {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestFacebookGraphClientExchangeUserAccessTokenUsesDefaultVersion(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v21.0/oauth/access_token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"access_token":"long-token"}`))
	}))
	defer srv.Close()

	client := newFacebookGraphClientWithClient(srv.URL, srv.Client())
	_, err := client.ExchangeUserAccessToken(context.Background(), "", "app-id", "app-secret", "short-token")
	if err != nil {
		t.Fatalf("exchange failed: %v", err)
	}
}

func TestFacebookGraphClientExchangeUserAccessTokenUnauthorized(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid OAuth access token.","code":190}}`))
	}))
	defer srv.Close()

	client := newFacebookGraphClientWithClient(srv.URL, srv.Client())
	_, err := client.ExchangeUserAccessToken(context.Background(), "v21.0", "app-id", "app-secret", "bad")
	if err == nil {
		t.Fatalf("expected error")
	}
	if err != ErrUnauthorized {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestFacebookGraphClientListManagedPagesSuccessWithPaging(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v21.0/me/accounts":
			query := r.URL.Query()
			if query.Get("access_token") != "user-token" {
				t.Fatalf("unexpected access token")
			}
			if query.Get("fields") != facebookListPagesFields {
				t.Fatalf("unexpected fields: %s", query.Get("fields"))
			}
			next := srv.URL + "/next?page=2"
			_, _ = w.Write([]byte(`{"data":[{"id":"p1","name":"Page One","category":"Brand","username":"pageone","access_token":"pt1"}],"paging":{"next":"` + next + `"}}`))
		case "/next":
			if r.URL.Query().Get("page") != "2" {
				t.Fatalf("unexpected next page query")
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"p2","name":"Page Two","access_token":"pt2"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := newFacebookGraphClientWithClient(srv.URL, srv.Client())
	pages, err := client.ListManagedPages(context.Background(), "v21.0", "user-token")
	if err != nil {
		t.Fatalf("list pages failed: %v", err)
	}

	if len(pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(pages))
	}
	if pages[0].ID != "p1" || pages[0].Name != "Page One" || pages[0].AccessToken != "pt1" {
		t.Fatalf("unexpected first page: %+v", pages[0])
	}
	if pages[1].ID != "p2" || pages[1].Name != "Page Two" || pages[1].AccessToken != "pt2" {
		t.Fatalf("unexpected second page: %+v", pages[1])
	}
}

func TestFacebookGraphClientListManagedPagesUnauthorized(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid OAuth access token.","code":190}}`))
	}))
	defer srv.Close()

	client := newFacebookGraphClientWithClient(srv.URL, srv.Client())
	_, err := client.ListManagedPages(context.Background(), "v21.0", "bad-token")
	if err == nil {
		t.Fatalf("expected error")
	}
	if err != ErrUnauthorized {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestFacebookGraphClientListManagedPagesRequiresToken(t *testing.T) {
	t.Parallel()

	client := NewFacebookGraphClient()
	_, err := client.ListManagedPages(context.Background(), "v21.0", "  ")
	if err == nil {
		t.Fatalf("expected error")
	}
	if err != ErrUnauthorized {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestFacebookGraphClientExchangeUserAccessTokenRequiresCredentials(t *testing.T) {
	t.Parallel()

	client := NewFacebookGraphClient()
	_, err := client.ExchangeUserAccessToken(context.Background(), "v21.0", "", "", "short")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("expected credentials error, got %v", err)
	}
}

func TestFacebookGraphClientExchangeUserAccessTokenEncodesQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := url.ParseQuery(r.URL.RawQuery); err != nil {
			t.Fatalf("query not parseable: %v", err)
		}
		_, _ = w.Write([]byte(`{"access_token":"ok"}`))
	}))
	defer srv.Close()

	client := newFacebookGraphClientWithClient(srv.URL, srv.Client())
	_, err := client.ExchangeUserAccessToken(context.Background(), "v21.0", "app", "secret", "token")
	if err != nil {
		t.Fatalf("exchange failed: %v", err)
	}
}
