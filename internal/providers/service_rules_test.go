package providers

import "testing"

func TestKnownProvider(t *testing.T) {
	t.Parallel()

	if !KnownProvider("facebook_page") {
		t.Fatalf("expected facebook_page to be a known provider")
	}
	if !KnownProvider("Twitter") {
		t.Fatalf("expected case-insensitive provider matching")
	}
	if KnownProvider("linkedin") {
		t.Fatalf("did not expect linkedin to be known yet")
	}
}

func TestServiceBindingForProvider(t *testing.T) {
	t.Parallel()

	binding, ok := ServiceBindingForProvider("facebook_page")
	if !ok {
		t.Fatalf("expected facebook_page binding")
	}
	if binding.Service != "facebook" || !binding.Required {
		t.Fatalf("unexpected binding: %+v", binding)
	}

	binding, ok = ServiceBindingForProvider("bluesky")
	if !ok {
		t.Fatalf("expected bluesky binding")
	}
	if binding.Service != "bluesky" || binding.Required {
		t.Fatalf("unexpected bluesky binding: %+v", binding)
	}
}

func TestValidateServiceConfigurationFacebookActive(t *testing.T) {
	t.Parallel()

	err := ValidateServiceConfiguration("facebook", map[string]any{}, true)
	if err == nil {
		t.Fatalf("expected validation error for missing client fields")
	}

	err = ValidateServiceConfiguration("facebook", map[string]any{
		"client_id":     "id",
		"client_secret": "secret",
		"api_version":   "v21.0",
	}, true)
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateServiceConfigurationFacebookAPIVersion(t *testing.T) {
	t.Parallel()

	err := ValidateServiceConfiguration("facebook", map[string]any{
		"api_version": "21",
	}, false)
	if err == nil {
		t.Fatalf("expected invalid api version error")
	}
}
