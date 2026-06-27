module github.com/lambogreny/jetder-mcp

go 1.23.0

require (
	github.com/jetder-core/api v0.0.0-00010101000000-000000000000
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

// Local development: the jetder-core/api repo has no published tags yet and is
// private, so we point at the parent module on disk. Replace with a pinned
// remote version (and set GOPRIVATE=github.com/jetder-core/*) once it tags.
replace github.com/jetder-core/api => ../
