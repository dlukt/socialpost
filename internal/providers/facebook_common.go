package providers

// ResolveFacebookAPIVersion returns configured API version or default fallback.
func ResolveFacebookAPIVersion(configuration map[string]any) string {
	if apiVersion := lookupString(configuration, "api_version"); apiVersion != "" {
		return apiVersion
	}
	return defaultFacebookAPIVersion
}
