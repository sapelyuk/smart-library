module github.com/sapelyuk/smart-library/services/ai-service

go 1.26.0

require (
	github.com/google/uuid v1.6.0
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.30.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/sapelyuk/smart-library/pkg v0.0.0
	github.com/sapelyuk/smart-library/services/book-service v0.0.0
	github.com/sapelyuk/smart-library/services/user-service v0.0.0
	google.golang.org/genproto/googleapis/api v0.0.0-20260803160001-6ac0973c030d
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/rabbitmq/amqp091-go v1.15.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
)

replace github.com/sapelyuk/smart-library/pkg => ../../pkg

replace github.com/sapelyuk/smart-library/services/book-service => ../book-service

replace github.com/sapelyuk/smart-library/services/user-service => ../user-service
