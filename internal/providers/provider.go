package providers

import "context"

// Account is the provider-facing account representation.
type Account struct {
	ID          int64
	Provider    string
	ProviderID  string
	Name        string
	Username    string
	Data        map[string]any
	AccessToken map[string]any
}

// PublishRequest holds normalized post content for providers.
type PublishRequest struct {
	Text                 string
	ServiceConfiguration map[string]any
}

// PublishResult holds provider publish output.
type PublishResult struct {
	ProviderPostID string
	Data           map[string]any
}

// Connector implements provider-specific publish behavior.
type Connector interface {
	Provider() string
	Publish(ctx context.Context, account Account, request PublishRequest) (PublishResult, error)
}
