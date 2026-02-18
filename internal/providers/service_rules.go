package providers

import (
	"fmt"
	"regexp"
	"strings"
)

var facebookAPIVersionPattern = regexp.MustCompile(`^v\d+\.\d+$`)

// ServiceBinding defines provider -> service dependency.
type ServiceBinding struct {
	Service  string
	Required bool
}

func KnownProvider(provider string) bool {
	switch normalizeName(provider) {
	case "facebook_page", "twitter", "mastodon", "bluesky":
		return true
	default:
		return false
	}
}

func ServiceBindingForProvider(provider string) (ServiceBinding, bool) {
	switch normalizeName(provider) {
	case "facebook_page":
		return ServiceBinding{Service: "facebook", Required: true}, true
	case "twitter":
		return ServiceBinding{Service: "twitter", Required: true}, true
	case "bluesky":
		return ServiceBinding{Service: "bluesky", Required: false}, true
	default:
		return ServiceBinding{}, false
	}
}

// ValidateServiceConfiguration validates service config payload.
func ValidateServiceConfiguration(serviceName string, configuration map[string]any, active bool) error {
	name := normalizeName(serviceName)
	cfg := configuration
	if cfg == nil {
		cfg = map[string]any{}
	}

	switch name {
	case "facebook":
		if active {
			if err := requireString(cfg, "client_id"); err != nil {
				return fmt.Errorf("facebook configuration invalid: %w", err)
			}
			if err := requireString(cfg, "client_secret"); err != nil {
				return fmt.Errorf("facebook configuration invalid: %w", err)
			}
		}

		if apiVersion := lookupString(cfg, "api_version"); apiVersion != "" {
			if !facebookAPIVersionPattern.MatchString(apiVersion) {
				return fmt.Errorf("facebook configuration invalid: api_version must match vNN.N")
			}
		}
	case "twitter":
		if active {
			if err := requireString(cfg, "client_id"); err != nil {
				return fmt.Errorf("twitter configuration invalid: %w", err)
			}
			if err := requireString(cfg, "client_secret"); err != nil {
				return fmt.Errorf("twitter configuration invalid: %w", err)
			}
		}
	}

	return nil
}

func requireString(values map[string]any, key string) error {
	if lookupString(values, key) == "" {
		return fmt.Errorf("%s is required", key)
	}
	return nil
}

func normalizeName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
