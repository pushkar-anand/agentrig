// Package channel defines the transport-agnostic interface that all agentrig
// channel implementations must satisfy.
package channel

import (
	"context"
	"time"
)

// Message is an inbound message from a user on a channel.
type Message struct {
	// ID is a unique identifier for this message, assigned by the channel.
	ID string
	// SessionID identifies the conversation. Stable across turns for the same
	// conversation (e.g. a Matrix room ID for a DM).
	SessionID string
	// UserID is the resolved external user identifier (e.g. a finagent UUID).
	UserID string
	// Text is the plain-text content of the message.
	Text string
	// Timestamp is when the message was sent.
	Timestamp time.Time
}

// Response is an outbound reply from the agent.
type Response struct {
	// Text is the reply content.
	Text string
	// Markdown indicates whether Text contains Markdown formatting. Channels
	// that support rich text will render it; others fall back to plain text.
	Markdown bool
}

// MessageHandler processes an inbound Message and returns a Response.
type MessageHandler func(ctx context.Context, msg Message) (Response, error)

// Channel is the interface all transport implementations must satisfy.
type Channel interface {
	// Start begins listening for messages and calls handler for each one.
	// It blocks until ctx is cancelled.
	Start(ctx context.Context, handler MessageHandler) error
	// Name returns a short identifier used in logs (e.g. "matrix", "cli").
	Name() string
}
