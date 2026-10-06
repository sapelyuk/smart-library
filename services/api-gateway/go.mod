module github.com/sapelyuk/smart-library/services/api-gateway

go 1.26.0

require (
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.30.0
	github.com/sapelyuk/smart-library/services/book-service v0.0.0
	github.com/sapelyuk/smart-library/services/user-service v0.0.0
	google.golang.org/genproto/googleapis/api v0.0.0-20260803160001-6ac0973c030d
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
)

require (
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
)

replace github.com/sapelyuk/smart-library/pkg => ../../pkg

replace github.com/sapelyuk/smart-library/services/book-service => ../book-service

replace github.com/sapelyuk/smart-library/services/user-service => ../user-service
