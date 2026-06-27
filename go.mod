module github.com/lambogreny/jetder-mcp

go 1.23.0

require (
	github.com/jetder-core/api v0.0.0-20251222122510-e4c734d325f7
	github.com/modelcontextprotocol/go-sdk v1.2.0
)

require (
	github.com/acoshift/arpc/v2 v2.2.0 // indirect
	github.com/asaskevich/govalidator v0.0.0-20230301143203-a9d515a09cc2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/jsonschema-go v0.3.0 // indirect
	github.com/moonrhythm/validator v1.3.0 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.30.0 // indirect
)

// github.com/jetder-core/api is public and pinned to a pseudo-version above, so
// no `replace` is needed and external `go run` / module fetch works without auth.
// For local development AGAINST uncommitted jetder-core/api changes, temporarily
// add `replace github.com/jetder-core/api => ../` — but do NOT commit it.
