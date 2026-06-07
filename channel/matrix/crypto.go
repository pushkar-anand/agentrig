package matrix

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
)

// setupCrypto initialises the mautrix CryptoHelper and wires it into the
// client. It must be called after the client's Syncer is set.
//
// When encryption is disabled (cfg.EncryptionEnabled == false) it is a no-op.
//
// On first run a new Olm account and device are created and registered with
// the homeserver. Subsequent runs reload the existing identity from the SQLite
// store at cfg.CryptoStorePath.
//
// If cfg.RecoveryKey is provided the bot auto-verifies its own device using
// the stored cross-signing keys — no manual emoji verification is required.
func setupCrypto(ctx context.Context, client *mautrix.Client, cfg Config) error {
	if !cfg.EncryptionEnabled {
		return nil
	}

	helper, err := cryptohelper.NewCryptoHelper(client, []byte(cfg.PickleKey), cfg.CryptoStorePath)
	if err != nil {
		return fmt.Errorf("create crypto helper: %w", err)
	}

	// Init registers the encrypted-event handler on the already-set syncer,
	// loads (or creates) the Olm account, and verifies device keys on the server.
	if err := helper.Init(ctx); err != nil {
		return fmt.Errorf("init crypto helper: %w", err)
	}

	// Wire helper into the client so that outgoing SendMessageEvent calls are
	// automatically encrypted in rooms that have encryption enabled.
	client.Crypto = helper

	if cfg.RecoveryKey != "" {
		if err := helper.Machine().VerifyWithRecoveryKey(ctx, cfg.RecoveryKey); err != nil {
			// Non-fatal: the bot can still operate without self-verification,
			// but encrypted DMs may require manual device trust from the user.
			client.Log.Warn().Err(err).Msg("Failed to verify own device with recovery key")
		}
	}

	return nil
}
