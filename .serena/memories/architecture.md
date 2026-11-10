# sel-ani architecture
Go TUI anime browser/player. Module `sel-ani`, entry `cmd/sel-ani/main.go`.
- internal/domain: core types and ports (interfaces). No deps on other internal pkgs.
- internal/app: use cases (search, latest, my list, episodes, quality, play). Defines Store interface. Imports only domain, never UI.
- internal/ui: bubbletea v2 + lipgloss v2 (model.go, view.go, styles.go, glyphs.go, config.go).
- internal/config: TOML config/credentials/theme (hand-rolled toml.go, edit.go). default.toml embedded.
- internal/infra/*: adapters. miruro (source scraper, devalue.go), anidb (UDP API, titles, tracker, match), store (sqlite via modernc), cache, fetch (+rod headless browser), hls, player (mpv/download), image, kitty (graphics protocol).
Rule: dependencies point inward (infra -> domain, app -> domain, ui -> app/domain).
Testdata under */testdata is fixtures (ignored by Serena).
