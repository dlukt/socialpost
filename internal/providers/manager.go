package providers

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrUnsupportedProvider  = errors.New("unsupported provider")
	ErrUnauthorized         = errors.New("provider unauthorized")
	ErrInvalidContent       = errors.New("invalid publish content")
	ErrServiceDisabled      = errors.New("service disabled")
	ErrServiceNotConfigured = errors.New("service not configured")
)

// Manager resolves provider connector modules.
type Manager struct {
	connectors map[string]Connector
}

func NewManager(connectors ...Connector) *Manager {
	index := make(map[string]Connector, len(connectors))
	for _, connector := range connectors {
		index[connector.Provider()] = connector
	}
	return &Manager{connectors: index}
}

func NewDefaultManager() *Manager {
	return NewManager(
		newFacebookPageConnector(),
		newStubConnector("twitter"),
		newStubConnector("mastodon"),
		newStubConnector("bluesky"),
	)
}

func (m *Manager) Publish(ctx context.Context, account Account, request PublishRequest) (PublishResult, error) {
	connector, ok := m.connectors[account.Provider]
	if !ok {
		return PublishResult{}, fmt.Errorf("%w: %s", ErrUnsupportedProvider, account.Provider)
	}
	return connector.Publish(ctx, account, request)
}
