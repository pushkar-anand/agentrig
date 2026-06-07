# agentrig

A Go library providing reusable channel connectors for AI agents. Import a connector, pass your config, and your agent is reachable on that platform — no boilerplate.

```go
ch, _ := matrix.New(cfg)
ch.Start(ctx, agent.HandleMessage)
```

## Channels

| Channel | Package | Encryption |
|---------|---------|------------|
| Matrix | `channel/matrix` | E2E (Olm/Megolm via goolm) |

More channels planned (Telegram, Signal).

## Installation

```bash
go get github.com/pushkar-anand/agentrig
```

Build with the `goolm` tag to use the pure-Go Olm implementation (no `libolm` system dependency required):

```bash
go build -tags goolm ./...
```

## Usage

### 1. Implement `channel.MessageHandler`

Your agent needs to satisfy one function signature:

```go
func(ctx context.Context, msg channel.Message) (channel.Response, error)
```

`Message` carries the sender's external user ID, a stable session ID, and the message text. `Response` carries the reply text and a `Markdown` flag.

### 2. Configure and start a channel

```go
package main

import (
    "context"
    "log"

    "github.com/pushkar-anand/agentrig/channel"
    "github.com/pushkar-anand/agentrig/channel/matrix"
)

func main() {
    cfg := matrix.Config{
        HomeserverURL: "https://matrix.example.com",
        UserID:        "@mybot:example.com",
        AccessToken:   "syt_...",

        EncryptionEnabled: true,
        CryptoStorePath:   "/var/lib/mybot/crypto.db",
        PickleKey:         "a-secret-pickle-key",
        RecoveryKey:       "EsT...", // from Element → Settings → Security & Privacy

        AllowedUsers: []string{"@alice:example.com", "@partner:example.com"},
        Users: map[string]string{
            "@alice:example.com":   "uuid-alice",
            "@partner:example.com": "uuid-partner",
        },
    }

    ch, err := matrix.New(cfg)
    if err != nil {
        log.Fatal(err)
    }

    if err := ch.Start(context.Background(), myHandler); err != nil {
        log.Fatal(err)
    }
}

func myHandler(ctx context.Context, msg channel.Message) (channel.Response, error) {
    // msg.UserID    → your internal user ID (e.g. a UUID from your DB)
    // msg.SessionID → stable conversation ID (Matrix room ID for DMs)
    return channel.Response{
        Text:     "Hello from my agent!",
        Markdown: false,
    }, nil
}
```

### Config via koanf

`Config` structs carry `koanf:"..."` tags. If your agent uses [koanf](https://github.com/knadh/koanf) for config loading, unmarshal directly:

```go
var cfg matrix.Config
k.Unmarshal("channel.matrix", &cfg)
```

Example `config.yaml` section:

```yaml
channel:
  matrix:
    homeserver_url: "https://matrix.example.com"
    user_id: "@mybot:example.com"
    access_token: "syt_..."
    encryption_enabled: true
    crypto_store_path: "/var/lib/mybot/crypto.db"
    pickle_key: "a-secret-pickle-key"
    recovery_key: "EsT..."
    allowed_users:
      - "@alice:example.com"
      - "@partner:example.com"
    users:
      "@alice:example.com": "uuid-alice"
      "@partner:example.com": "uuid-partner"
```

## Matrix channel

### How it works

- The bot connects to the homeserver using an access token and enters the Matrix sync loop.
- Each DM from an allowed user is routed to your `MessageHandler` with the sender mapped to an external user ID via `Config.Users`.
- The Matrix room ID is used as `SessionID` — stable for the lifetime of the DM, so conversation sessions persist across bot restarts.
- Messages from unlisted senders are silently dropped.
- Concurrent messages in the same room are serialised so your handler is never called concurrently for the same conversation.
- A typing indicator is sent while the handler runs.
- Replies with `Markdown: true` are rendered to HTML and sent as formatted Matrix messages.

### End-to-end encryption

agentrig uses [mautrix-go](https://github.com/mautrix/go) with the `goolm` build tag (pure-Go Olm — no `libolm` system library needed).

**One-time setup:**

1. Create a dedicated bot account on your homeserver.
2. Obtain an access token from Element: *Settings → Help & About → Advanced → Access Token*.
3. Log in as the bot in Element, then go to *Settings → Security & Privacy → Encryption → Set up Secure Backup*. Generate and save the recovery key (`EsT...`).
4. Add the recovery key to config as `recovery_key`. On each startup the bot auto-verifies its own device — no manual emoji verification needed.

**Crypto store:**

Keys and sessions are persisted in a SQLite database at `crypto_store_path`. **Do not delete this file** after first run — losing it means losing the bot's device identity and requiring all users to re-trust the device.

**User trust:**

After the first run, the bot's device will appear in each user's DM in Element. Users need to mark it as trusted once (or enable cross-signing so trust propagates automatically).

## Building

```bash
# Build
make build        # go build -tags goolm ./...

# Vet
make vet          # go vet -tags goolm ./...

# Unit tests (no homeserver needed)
make test         # go test -tags goolm -race -count=1 ./...

# Integration tests (requires a live Matrix server)
MATRIX_HOMESERVER=https://... \
MATRIX_ACCESS_TOKEN=syt_... \
  make test-integration

# Lint
make lint         # staticcheck
```

## Implementing a custom channel

Implement the `channel.Channel` interface:

```go
type Channel interface {
    Start(ctx context.Context, handler MessageHandler) error
    Name() string
}
```

`Start` should block until `ctx` is cancelled, calling `handler` for each inbound message and sending the response back to the user.

## License

MIT
