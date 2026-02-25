module github.com/yourflock/roost/services/relay

go 1.24.0

require (
	github.com/google/uuid v1.6.0
	github.com/lib/pq v1.10.9
	github.com/yourflock/roost v0.0.0
)

require github.com/golang-jwt/jwt/v5 v5.3.1 // indirect

replace github.com/yourflock/roost => ../../
