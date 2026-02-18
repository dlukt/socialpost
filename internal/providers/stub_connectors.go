package providers

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type stubConnector struct {
	provider string
}

func newStubConnector(provider string) *stubConnector {
	return &stubConnector{provider: provider}
}

func (c *stubConnector) Provider() string {
	return c.provider
}

func (c *stubConnector) Publish(_ context.Context, account Account, request PublishRequest) (PublishResult, error) {
	if strings.TrimSpace(request.Text) == "" {
		return PublishResult{}, ErrInvalidContent
	}

	if value, ok := account.AccessToken["unauthorized"]; ok {
		if unauthorized, ok := value.(bool); ok && unauthorized {
			return PublishResult{}, ErrUnauthorized
		}
	}

	providerPostID := fmt.Sprintf("%s_%s_%d", c.provider, account.ProviderID, time.Now().UTC().UnixNano())
	return PublishResult{
		ProviderPostID: providerPostID,
		Data: map[string]any{
			"provider":  c.provider,
			"stub":      true,
			"posted_at": time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}
