package events

import (
	"errors"
	"strings"

	"github.com/sapelyuk/smart-library/pkg/config"
)

// Environment variables of the transport configuration.
const (
	// EnvURL is the AMQP connection string of the broker.
	EnvURL = "RABBITMQ_URL"

	// EnvExchange is the topic exchange every domain event is published to.
	EnvExchange = "RABBITMQ_EXCHANGE"
)

// Defaults used when the environment leaves a value out. They match the local
// RabbitMQ of docker-compose.yml, so a service started on the host needs no
// extra configuration.
const (
	DefaultURL      = "amqp://guest:guest@localhost:5672/"
	DefaultExchange = "library.events"
)

// Config is the broker configuration the transport is built with.
type Config struct {
	// URL is the AMQP connection string, e.g.
	// amqp://user:password@host:5672/vhost.
	URL string

	// Exchange is the topic exchange domain events travel through. ADR-0001
	// fixes a single exchange for the whole platform.
	Exchange string
}

// ConfigFromEnv reads the transport configuration from the environment, falling
// back to the local defaults. Every variable is read through pkg/config, so the
// service binaries stay configured the same way everywhere.
func ConfigFromEnv() Config {
	return Config{
		URL:      config.String(EnvURL, DefaultURL),
		Exchange: config.String(EnvExchange, DefaultExchange),
	}
}

// Validate reports whether the configuration can open a connection.
func (c Config) Validate() error {
	if strings.TrimSpace(c.URL) == "" {
		return errors.New("events: broker URL is empty")
	}

	if strings.TrimSpace(c.Exchange) == "" {
		return errors.New("events: exchange name is empty")
	}

	return nil
}
