# Commands (mise.toml)
- mise run lint        gofmt check + go vet
- mise run test:short  unit tests, skips headless browser test
- mise run test        go test -race ./...
- mise run build       static binary to dist/sel-ani
- mise run cover       coverage summary
Toolchain pinned in mise.toml (go 1.27.1, gopls latest). gopls lives under ~/.local/share/mise/installs; Serena needs it on PATH (run Claude via `mise exec` or activate mise).
Local-only files (gitignored): config.toml, credentials.toml (must be chmod 600).
