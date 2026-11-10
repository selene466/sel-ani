# Conventions
- Package doc comment on each package; exported identifiers documented.
- Tests alongside code (*_test.go), table style, fixtures in testdata/.
- CGO disabled; pure-Go deps only (modernc sqlite).
- AniDB UDP rate limit ~1 packet/4s: keep sync batched (see app.SyncBatch).
- gofmt + go vet must pass before done.
