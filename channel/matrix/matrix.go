// Package matrix implements a Matrix channel for agentrig.
//
// Each household member DMs the bot; the Matrix room ID is used as the
// conversation session ID, and the sender's Matrix user ID is mapped to an
// external user ID via Config.Users before being forwarded to the handler.
//
// End-to-end encryption is supported via mautrix's CryptoHelper (goolm build
// tag enables pure-Go Olm with no libolm system dependency).
package matrix

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/renderer/html"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/pushkar-anand/agentrig/channel"
)

// Matrix is a Matrix channel that routes DMs to a MessageHandler.
type Matrix struct {
	cfg    Config
	client *mautrix.Client
	md     goldmark.Markdown
	rooms  sync.Map // map[id.RoomID → *sync.Mutex]: serialises per-room handling
}

// New creates a Matrix channel from cfg. Call Start to begin receiving messages.
func New(cfg Config) (*Matrix, error) {
	client, err := mautrix.NewClient(cfg.HomeserverURL, id.UserID(cfg.UserID), cfg.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("create matrix client: %w", err)
	}

	return &Matrix{cfg: cfg, client: client, md: newMarkdown()}, nil
}

// Name implements channel.Channel.
func (m *Matrix) Name() string { return "matrix" }

// Start implements channel.Channel. It blocks until ctx is cancelled.
//
// It resolves the bot's device ID via /whoami, initialises encryption (if
// enabled), registers event handlers, and enters the sync loop.
func (m *Matrix) Start(ctx context.Context, handler channel.MessageHandler) error {
	// Resolve device ID from the access token — required by CryptoHelper.
	whoami, err := m.client.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("whoami: %w", err)
	}
	m.client.DeviceID = whoami.DeviceID

	syncer := mautrix.NewDefaultSyncer()
	m.client.Syncer = syncer

	// setupCrypto must be called after Syncer is set so that Init() can
	// register the encrypted-event handler on the syncer.
	if err := setupCrypto(ctx, m.client, m.cfg); err != nil {
		return fmt.Errorf("setup crypto: %w", err)
	}

	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		m.handleMessage(ctx, evt, handler)
	})

	return m.client.SyncWithContext(ctx)
}

// handleMessage processes a single inbound EventMessage.
func (m *Matrix) handleMessage(ctx context.Context, evt *event.Event, handler channel.MessageHandler) {
	log := zerolog.Ctx(ctx)

	// Ignore own messages (echo suppression).
	if evt.Sender == id.UserID(m.cfg.UserID) {
		return
	}

	sender := evt.Sender.String()

	// Access control: drop messages from unlisted senders.
	if !slices.Contains(m.cfg.AllowedUsers, sender) {
		return
	}

	// User mapping: drop if sender has no external user ID.
	userID, ok := m.cfg.Users[sender]
	if !ok {
		return
	}

	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		return
	}
	if content.MsgType != event.MsgText {
		return
	}

	// Serialise handling per room so concurrent messages from the same room
	// don't produce interleaved tool calls inside the agent.
	mu := m.roomMu(evt.RoomID)
	mu.Lock()
	defer mu.Unlock()

	// Typing indicator while the handler runs (no-op when client is nil, e.g. in tests).
	if m.client != nil {
		if _, err := m.client.UserTyping(ctx, evt.RoomID, true, 30*time.Second); err != nil {
			log.Warn().Err(err).Msg("failed to send typing notification")
		}
		defer func() {
			if _, err := m.client.UserTyping(ctx, evt.RoomID, false, 0); err != nil {
				log.Warn().Err(err).Msg("failed to clear typing notification")
			}
		}()
	}

	msg := channel.Message{
		ID:        evt.ID.String(),
		SessionID: evt.RoomID.String(),
		UserID:    userID,
		Text:      content.Body,
		Timestamp: time.UnixMilli(evt.Timestamp),
	}

	resp, err := handler(ctx, msg)
	if err != nil {
		log.Error().Err(err).Str("room", evt.RoomID.String()).Msg("handler error")
		if m.client != nil {
			if _, sendErr := m.client.SendText(ctx, evt.RoomID, "Sorry, something went wrong."); sendErr != nil {
				log.Error().Err(sendErr).Msg("failed to send error message")
			}
		}
		return
	}

	if m.client != nil {
		if err := m.sendResponse(ctx, evt.RoomID, resp); err != nil {
			log.Error().Err(err).Str("room", evt.RoomID.String()).Msg("failed to send response")
		}
	}
}

// roomMu returns (or creates) the mutex that serialises handling for roomID.
func (m *Matrix) roomMu(roomID id.RoomID) *sync.Mutex {
	v, _ := m.rooms.LoadOrStore(roomID, &sync.Mutex{})
	return v.(*sync.Mutex) //nolint:forcetypeassert
}

// sendResponse sends resp back to roomID, rendering Markdown to HTML when supported.
func (m *Matrix) sendResponse(ctx context.Context, roomID id.RoomID, resp channel.Response) error {
	content, err := m.buildContent(resp)
	if err != nil {
		// buildContent already fell back to plain text; send that.
		_, err = m.client.SendText(ctx, roomID, resp.Text)
		return err
	}
	if content.Format == "" {
		// Plain text — use the simpler SendText path.
		_, err = m.client.SendText(ctx, roomID, content.Body)
		return err
	}
	_, err = m.client.SendMessageEvent(ctx, roomID, event.EventMessage, content)
	return err
}

// buildContent converts a Response into a Matrix message event content struct.
// For Markdown responses it renders to HTML; on render failure it falls back to
// plain text and returns an error so the caller can decide how to proceed.
func (m *Matrix) buildContent(resp channel.Response) (*event.MessageEventContent, error) {
	if !resp.Markdown {
		return &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    resp.Text,
		}, nil
	}

	var buf bytes.Buffer
	if err := m.md.Convert([]byte(resp.Text), &buf); err != nil {
		return &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    resp.Text,
		}, err
	}

	return &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          resp.Text,
		Format:        event.FormatHTML,
		FormattedBody: buf.String(),
	}, nil
}

// newMarkdown returns the goldmark instance used for rendering.
func newMarkdown() goldmark.Markdown {
	return goldmark.New(goldmark.WithRendererOptions(html.WithUnsafe()))
}
