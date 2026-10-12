package email

import "errors"

var (
	// ErrDeliveryFailed reports that the SMTP server could not deliver the email.
	ErrDeliveryFailed = errors.New("email: delivery failed")
)
