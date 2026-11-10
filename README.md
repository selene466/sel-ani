# sel-ani

A vibecoding slopp for my personal use.

Terminal UI to browse, search and play from Miruro.  
Tracks watch history locally and can sync list with AniDB.

## Install

With [mise](https://mise.jdx.dev):

```
mise use -g github:selene466/sel-ani@latest
```

Or build from source (Go 1.27+):

```
mise run build
```

Writes `./dist/sel-ani`.

## Requirements

- A player: mpv by default (vlc, iina, syncplay, celluloid, haruna, mplayer also work).
- A Chromium-based browser, used headless to fetch some pages.
- A terminal with kitty graphics support for covers (optional, `ui.cover = false` to disable).
- A Nerd Font for the default glyphs (optional, `ui.font = "ascii"` to disable).

## Usage

```
sel-ani
sel-ani --config path/to/config.toml
sel-ani --dmenu
sel-ani --rofi
sel-ani --version
```

### dmenu and rofi

`--dmenu` and `--rofi` skip the TUI and run as a menu.

## Configuration

On first run a default `config.toml` is created at `~/.config/sel-ani/config.toml` (or `$XDG_CONFIG_HOME/sel-ani/`). See [config.example.toml](config.example.toml) for every option.

AniDB sync is optional, setup with wizard `anidb.account` in Config menu.  
Alternatively copy [credentials.example.toml](credentials.example.toml) to `credentials.toml`, config set `[anidb] enabled = true`, and `chmod 600` the credentials file.  
sel-ani refuses to read it otherwise.

## Development

```
mise run lint
mise run test:short
mise run test
```

## License

MIT
