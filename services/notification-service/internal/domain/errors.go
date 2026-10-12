package domain

import "errors"

var (
	// ErrInvalidRecipient reports that the notification has no delivery target.
	ErrInvalidRecipient = errors.New("notification: no valid recipient (email or phone)")

	// ErrDeliveryFailed reports that the transport could not send the notification.
	ErrDeliveryFailed = errors.New("notification: delivery failed")
)
