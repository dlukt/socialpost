package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const facebookListPagesFields = "id,name,category,username,access_token"

type FacebookPage struct {
	ID          string
	Name        string
	Category    string
	Username    string
	AccessToken string
}

type FacebookGraphClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewFacebookGraphClient() *FacebookGraphClient {
	return newFacebookGraphClientWithClient("https://graph.facebook.com", &http.Client{Timeout: 20 * time.Second})
}

func newFacebookGraphClientWithClient(baseURL string, client *http.Client) *FacebookGraphClient {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &FacebookGraphClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: client,
	}
}

func (c *FacebookGraphClient) ExchangeUserAccessToken(ctx context.Context, apiVersion, clientID, clientSecret, shortLivedToken string) (string, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return "", fmt.Errorf("facebook app credentials are required")
	}
	if strings.TrimSpace(shortLivedToken) == "" {
		return "", ErrUnauthorized
	}

	version := strings.TrimSpace(apiVersion)
	if version == "" {
		version = defaultFacebookAPIVersion
	}

	params := url.Values{}
	params.Set("grant_type", "fb_exchange_token")
	params.Set("client_id", strings.TrimSpace(clientID))
	params.Set("client_secret", strings.TrimSpace(clientSecret))
	params.Set("fb_exchange_token", strings.TrimSpace(shortLivedToken))

	endpoint := fmt.Sprintf("%s/%s/oauth/access_token?%s", c.baseURL, version, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		graphErr := parseFacebookError(body)
		if graphErr.Error.Code == 190 {
			return "", ErrUnauthorized
		}
		if graphErr.Error.Message != "" {
			return "", fmt.Errorf("facebook token exchange failed: %s", graphErr.Error.Message)
		}
		return "", fmt.Errorf("facebook token exchange failed with status %d", resp.StatusCode)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("facebook token exchange decode failed: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return "", fmt.Errorf("facebook token exchange failed: missing access_token")
	}

	return strings.TrimSpace(payload.AccessToken), nil
}

func (c *FacebookGraphClient) ExchangeAuthorizationCode(ctx context.Context, apiVersion, clientID, clientSecret, redirectURI, code string) (string, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return "", fmt.Errorf("facebook app credentials are required")
	}
	if strings.TrimSpace(redirectURI) == "" {
		return "", fmt.Errorf("redirect_uri is required")
	}
	if strings.TrimSpace(code) == "" {
		return "", ErrUnauthorized
	}

	version := strings.TrimSpace(apiVersion)
	if version == "" {
		version = defaultFacebookAPIVersion
	}

	params := url.Values{}
	params.Set("client_id", strings.TrimSpace(clientID))
	params.Set("client_secret", strings.TrimSpace(clientSecret))
	params.Set("redirect_uri", strings.TrimSpace(redirectURI))
	params.Set("code", strings.TrimSpace(code))

	endpoint := fmt.Sprintf("%s/%s/oauth/access_token?%s", c.baseURL, version, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		graphErr := parseFacebookError(body)
		if graphErr.Error.Code == 190 {
			return "", ErrUnauthorized
		}
		if graphErr.Error.Message != "" {
			return "", fmt.Errorf("facebook code exchange failed: %s", graphErr.Error.Message)
		}
		return "", fmt.Errorf("facebook code exchange failed with status %d", resp.StatusCode)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("facebook code exchange decode failed: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return "", fmt.Errorf("facebook code exchange failed: missing access_token")
	}

	return strings.TrimSpace(payload.AccessToken), nil
}

func (c *FacebookGraphClient) ListManagedPages(ctx context.Context, apiVersion, userAccessToken string) ([]FacebookPage, error) {
	if strings.TrimSpace(userAccessToken) == "" {
		return nil, ErrUnauthorized
	}

	version := strings.TrimSpace(apiVersion)
	if version == "" {
		version = defaultFacebookAPIVersion
	}

	params := url.Values{}
	params.Set("fields", facebookListPagesFields)
	params.Set("access_token", strings.TrimSpace(userAccessToken))
	endpoint := fmt.Sprintf("%s/%s/me/accounts?%s", c.baseURL, version, params.Encode())

	pages := make([]FacebookPage, 0)
	for i := 0; endpoint != "" && i < 20; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			graphErr := parseFacebookError(body)
			if graphErr.Error.Code == 190 {
				return nil, ErrUnauthorized
			}
			if graphErr.Error.Message != "" {
				return nil, fmt.Errorf("facebook list pages failed: %s", graphErr.Error.Message)
			}
			return nil, fmt.Errorf("facebook list pages failed with status %d", resp.StatusCode)
		}

		var payload struct {
			Data []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Category    string `json:"category"`
				Username    string `json:"username"`
				AccessToken string `json:"access_token"`
			} `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("facebook list pages decode failed: %w", err)
		}

		for _, row := range payload.Data {
			pages = append(pages, FacebookPage{
				ID:          strings.TrimSpace(row.ID),
				Name:        strings.TrimSpace(row.Name),
				Category:    strings.TrimSpace(row.Category),
				Username:    strings.TrimSpace(row.Username),
				AccessToken: strings.TrimSpace(row.AccessToken),
			})
		}

		endpoint = strings.TrimSpace(payload.Paging.Next)
	}

	return pages, nil
}
