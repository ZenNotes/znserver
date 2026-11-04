module github.com/ZenNotes/znserver

go 1.26.0

require (
	github.com/coder/websocket v1.8.12
	github.com/fsnotify/fsnotify v1.8.0
	github.com/go-chi/chi/v5 v5.4.0
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/modelcontextprotocol/go-sdk v1.7.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

require (
	github.com/ZenNotes/tui v0.17.0
	golang.org/x/sys v0.47.0 // indirect
)

// Local until tui is tagged. This cannot be released, and CI cannot build it.
replace github.com/ZenNotes/tui => ../../zennotescli/.claude/worktrees/design-http-mcp-56-a234c1
