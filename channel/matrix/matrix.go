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
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/pushkar-anand/agentrig/channel"
)

// Option configures a Matrix channel.
type Option func(*Matrix)

// WithLogger sets the slog.Logger used for internal log output.
// If not provided, slog.Default() is used.
func WithLogger(l *slog.Logger) Option {
	return func(m *Matrix) { m.log = l }
}

// Matrix is a Matrix channel that routes DMs to a MessageHandler.
type Matrix struct {
	cfg    Config
	client *mautrix.Client
	md     goldmark.Markdown
	log    *slog.Logger
	rooms  sync.Map // map[id.RoomID → *sync.Mutex]: serialises per-room handling
}

// New creates a Matrix channel from cfg. Call Start to begin receiving messages.
func New(cfg Config, opts ...Option) (*Matrix, error) {
	client, err := mautrix.NewClient(cfg.HomeserverURL, id.UserID(cfg.UserID), cfg.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("create matrix client: %w", err)
	}

	m := &Matrix{cfg: cfg, client: client, md: newMarkdown(), log: slog.Default()}
	for _, o := range opts {
		o(m)
	}
	return m, nil
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

	syncer.OnEventType(event.StateMember, func(ctx context.Context, evt *event.Event) {
		m.handleInvite(ctx, evt)
	})

	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		m.handleMessage(ctx, evt, handler)
	})

	return m.client.SyncWithContext(ctx)
}

// handleInvite auto-accepts room invites from allowed users.
func (m *Matrix) handleInvite(ctx context.Context, evt *event.Event) {
	content, ok := evt.Content.Parsed.(*event.MemberEventContent)
	if !ok || content.Membership != event.MembershipInvite {
		return
	}
	// Only the invite addressed to the bot matters.
	if evt.GetStateKey() != m.cfg.UserID {
		return
	}
	if !slices.Contains(m.cfg.AllowedUsers, evt.Sender.String()) {
		m.log.WarnContext(ctx, "ignoring invite from unlisted user", "sender", evt.Sender.String())
		return
	}
	if _, err := m.client.JoinRoomByID(ctx, evt.RoomID); err != nil {
		m.log.ErrorContext(ctx, "failed to join room", "room", evt.RoomID.String(), "err", err)
	}
}

// handleMessage processes a single inbound EventMessage.
func (m *Matrix) handleMessage(ctx context.Context, evt *event.Event, handler channel.MessageHandler) {
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

	var eyesEventID id.EventID
	if m.client != nil {
		// 👀 reaction signals the message was received and is being processed.
		if resp, err := m.client.SendReaction(ctx, evt.RoomID, evt.ID, "👀"); err != nil {
			m.log.WarnContext(ctx, "failed to send eyes reaction", "err", err)
		} else {
			eyesEventID = resp.EventID
		}
		if _, err := m.client.UserTyping(ctx, evt.RoomID, true, 30*time.Second); err != nil {
			m.log.WarnContext(ctx, "failed to send typing notification", "err", err)
		}
		defer func() {
			if _, err := m.client.UserTyping(ctx, evt.RoomID, false, 0); err != nil {
				m.log.WarnContext(ctx, "failed to clear typing notification", "err", err)
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
		m.log.ErrorContext(ctx, "handler error", "room", evt.RoomID.String(), "err", err)
		if m.client != nil {
			if _, sendErr := m.client.SendText(ctx, evt.RoomID, "Sorry, something went wrong."); sendErr != nil {
				m.log.ErrorContext(ctx, "failed to send error message", "err", sendErr)
			}
		}
		return
	}

	if m.client != nil {
		if err := m.sendResponse(ctx, evt.RoomID, resp); err != nil {
			m.log.ErrorContext(ctx, "failed to send response", "room", evt.RoomID.String(), "err", err)
			return
		}
		// Replace 👀 with ✅ once the response is delivered.
		if eyesEventID != "" {
			if _, err := m.client.RedactEvent(ctx, evt.RoomID, eyesEventID); err != nil {
				m.log.WarnContext(ctx, "failed to redact eyes reaction", "err", err)
			}
		}
		if _, err := m.client.SendReaction(ctx, evt.RoomID, evt.ID, "✅"); err != nil {
			m.log.WarnContext(ctx, "failed to send check reaction", "err", err)
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
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
}
