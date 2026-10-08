package events_test

import (
	"testing"

	"github.com/sapelyuk/smart-library/pkg/events"
)

func TestConfigFromEnvUsesDefaults(t *testing.T) {
	t.Setenv(events.EnvURL, "")
	t.Setenv(events.EnvExchange, "")

	cfg := events.ConfigFromEnv()

	if cfg.URL != events.DefaultURL {
		t.Errorf("URL = %q, want %q", cfg.URL, events.DefaultURL)
	}

	if cfg.Exchange != events.DefaultExchange {
		t.Errorf("Exchange = %q, want %q", cfg.Exchange, events.DefaultExchange)
	}
}

func TestConfigFromEnvReadsEnvironment(t *testing.T) {
	t.Setenv(events.EnvURL, "amqp://user:pass@broker:5672/")
	t.Setenv(events.EnvExchange, "library.events.test")

	cfg := events.ConfigFromEnv()

	if cfg.URL != "amqp://user:pass@broker:5672/" {
		t.Errorf("URL = %q, want the environment value", cfg.URL)
	}

	if cfg.Exchange != "library.events.test" {
		t.Errorf("Exchange = %q, want the environment value", cfg.Exchange)
	}
}

func TestConfigValidate(t *testing.T) {
	if err := (events.Config{URL: "amqp://localhost", Exchange: "library.events"}).Validate(); err != nil {
		t.Errorf("Validate(valid) = %v, want nil", err)
	}

	if err := (events.Config{Exchange: "library.events"}).Validate(); err == nil {
		t.Error("Validate(empty URL) = nil, want an error")
	}

	if err := (events.Config{URL: "amqp://localhost"}).Validate(); err == nil {
		t.Error("Validate(empty exchange) = nil, want an error")
	}
}
