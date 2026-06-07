package matrix

// Config holds all configuration for the Matrix channel.
//
// agentrig is a library — it does not load config itself. Callers use their
// own koanf instance and unmarshal into Config:
//
//	var cfg matrix.Config
//	k.Unmarshal("channel.matrix", &cfg)
type Config struct {
	// HomeserverURL is the base URL of the Matrix homeserver (e.g. https://matrix.example.com).
	HomeserverURL string `koanf:"homeserver_url"`
	// UserID is the fully-qualified Matrix user ID of the bot (e.g. @bot:example.com).
	UserID string `koanf:"user_id"`
	// AccessToken is the bot's Matrix access token (syt_...).
	// Preferred over password login; obtain once from Element or the login API.
	AccessToken string `koanf:"access_token"`

	// EncryptionEnabled enables end-to-end encryption for all rooms.
	EncryptionEnabled bool `koanf:"encryption_enabled"`
	// CryptoStorePath is the path to the SQLite database used to persist Olm
	// sessions, Megolm sessions, and cross-signing keys. Must not be deleted
	// after first run — doing so loses device identity and requires re-verification.
	CryptoStorePath string `koanf:"crypto_store_path"`
	// PickleKey is a secret used to encrypt the crypto store at rest.
	PickleKey string `koanf:"pickle_key"`
	// RecoveryKey is the SSSS recovery key (EsT... format, from Element
	// Settings → Security & Privacy → Encryption → Secure Backup).
	// When set, the bot auto-verifies its own device on startup via stored
	// cross-signing keys — no manual emoji verification needed.
	RecoveryKey string `koanf:"recovery_key"`

	// AllowedUsers is the list of Matrix user IDs permitted to interact with
	// the bot (e.g. [@alice:example.com, @partner:example.com]).
	// Messages from any other sender are silently dropped.
	AllowedUsers []string `koanf:"allowed_users"`

	// Users maps Matrix user IDs to external user IDs (e.g. finagent UUIDs).
	// Only senders present in this map are routed to the handler.
	// Example: {"@alice:example.com": "uuid-alice", "@partner:example.com": "uuid-partner"}
	Users map[string]string `koanf:"users"`
}
