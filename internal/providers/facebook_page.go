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

const defaultFacebookAPIVersion = "v21.0"

type facebookPageConnector struct {
	baseURL    string
	httpClient *http.Client
}

func newFacebookPageConnector() *facebookPageConnector {
	return newFacebookPageConnectorWithClient("https://graph.facebook.com", &http.Client{Timeout: 20 * time.Second})
}

func newFacebookPageConnectorWithClient(baseURL string, client *http.Client) *facebookPageConnector {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &facebookPageConnector{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: client,
	}
}

func (c *facebookPageConnector) Provider() string {
	return "facebook_page"
}

func (c *facebookPageConnector) Publish(ctx context.Context, account Account, request PublishRequest) (PublishResult, error) {
	message := strings.TrimSpace(request.Text)
	if message == "" {
		return PublishResult{}, ErrInvalidContent
	}

	pageID := strings.TrimSpace(account.ProviderID)
	if pageID == "" {
		return PublishResult{}, ErrInvalidContent
	}

	accessToken := firstNonEmptyString(
		lookupString(account.AccessToken, "page_access_token"),
		lookupString(account.AccessToken, "access_token"),
		lookupString(account.AccessToken, "token"),
	)
	if accessToken == "" {
		return PublishResult{}, ErrUnauthorized
	}

	apiVersion := firstNonEmptyString(
		lookupString(request.ServiceConfiguration, "api_version"),
		lookupString(account.Data, "api_version"),
		lookupString(account.AccessToken, "api_version"),
		defaultFacebookAPIVersion,
	)

	form := url.Values{}
	form.Set("message", message)
	form.Set("access_token", accessToken)

	endpoint := fmt.Sprintf("%s/%s/%s/feed", c.baseURL, apiVersion, url.PathEscape(pageID))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return PublishResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return PublishResult{}, err
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if err != nil {
		return PublishResult{}, err
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		graphErr := parseFacebookError(body)
		if httpResp.StatusCode == http.StatusUnauthorized || graphErr.Error.Code == 190 {
			return PublishResult{}, ErrUnauthorized
		}

		if graphErr.Error.Message != "" {
			return PublishResult{}, fmt.Errorf("facebook publish failed: %s", graphErr.Error.Message)
		}

		return PublishResult{}, fmt.Errorf("facebook publish failed with status %d", httpResp.StatusCode)
	}

	var response struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return PublishResult{}, fmt.Errorf("facebook response decode failed: %w", err)
	}
	if strings.TrimSpace(response.ID) == "" {
		return PublishResult{}, fmt.Errorf("facebook publish failed: missing id")
	}

	return PublishResult{
		ProviderPostID: response.ID,
		Data: map[string]any{
			"provider":    "facebook_page",
			"api_version": apiVersion,
		},
	}, nil
}

type facebookErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

func parseFacebookError(body []byte) facebookErrorEnvelope {
	var parsed facebookErrorEnvelope
	_ = json.Unmarshal(body, &parsed)
	return parsed
}

func lookupString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok {
		return ""
	}
	asString, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(asString)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
