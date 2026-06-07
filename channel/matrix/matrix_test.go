package matrix

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/pushkar-anand/agentrig/channel"
)

// testConfig returns a Config with two known users and no encryption.
func testConfig() Config {
	return Config{
		UserID:       "@bot:example.com",
		AllowedUsers: []string{"@alice:example.com", "@partner:example.com"},
		Users: map[string]string{
			"@alice:example.com":   "uuid-alice",
			"@partner:example.com": "uuid-partner",
		},
	}
}

// buildEvent constructs a minimal m.room.message text event.
func buildEvent(sender, roomID, body string) *event.Event {
	evt := &event.Event{
		ID:        id.EventID("$evt-" + sender),
		Sender:    id.UserID(sender),
		RoomID:    id.RoomID(roomID),
		Type:      event.EventMessage,
		Timestamp: time.Now().UnixMilli(),
	}
	evt.Content.Parsed = &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    body,
	}
	return evt
}

// countingHandler records calls and the last message received.
type countingHandler struct {
	calls atomic.Int64
	last  channel.Message
	mu    sync.Mutex
}

func (h *countingHandler) handle(_ context.Context, msg channel.Message) (channel.Response, error) {
	h.mu.Lock()
	h.last = msg
	h.mu.Unlock()
	h.calls.Add(1)
	return channel.Response{Text: "ok"}, nil
}

// newTestMatrix builds a Matrix suitable for unit tests (no live client).
func newTestMatrix(cfg Config) *Matrix {
	return &Matrix{cfg: cfg, md: newMarkdown()}
}

func TestOwnMessageDropped(t *testing.T) {
	m := newTestMatrix(testConfig())
	h := &countingHandler{}

	evt := buildEvent("@bot:example.com", "!room:example.com", "hello")
	m.handleMessage(t.Context(), evt, h.handle)

	if h.calls.Load() != 0 {
		t.Fatal("expected handler not to be called for own message")
	}
}

func TestUnknownSenderDropped(t *testing.T) {
	m := newTestMatrix(testConfig())
	h := &countingHandler{}

	evt := buildEvent("@stranger:example.com", "!room:example.com", "hello")
	m.handleMessage(t.Context(), evt, h.handle)

	if h.calls.Load() != 0 {
		t.Fatal("expected handler not to be called for sender not in AllowedUsers")
	}
}

func TestUnmappedSenderDropped(t *testing.T) {
	cfg := testConfig()
	// In AllowedUsers but NOT in Users map.
	cfg.AllowedUsers = append(cfg.AllowedUsers, "@ghost:example.com")
	m := newTestMatrix(cfg)
	h := &countingHandler{}

	evt := buildEvent("@ghost:example.com", "!room:example.com", "hello")
	m.handleMessage(t.Context(), evt, h.handle)

	if h.calls.Load() != 0 {
		t.Fatal("expected handler not to be called for sender without user mapping")
	}
}

func TestNonTextMessageDropped(t *testing.T) {
	m := newTestMatrix(testConfig())
	h := &countingHandler{}

	evt := buildEvent("@alice:example.com", "!room:example.com", "")
	evt.Content.Parsed = &event.MessageEventContent{MsgType: event.MsgImage}
	m.handleMessage(t.Context(), evt, h.handle)

	if h.calls.Load() != 0 {
		t.Fatal("expected handler not to be called for non-text message")
	}
}

func TestMessageRouted(t *testing.T) {
	m := newTestMatrix(testConfig())
	h := &countingHandler{}

	roomID := "!dm-alice:example.com"
	evt := buildEvent("@alice:example.com", roomID, "what did I spend on food?")
	m.handleMessage(t.Context(), evt, h.handle)

	if h.calls.Load() != 1 {
		t.Fatalf("expected handler to be called once, got %d", h.calls.Load())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.last.UserID != "uuid-alice" {
		t.Errorf("expected UserID uuid-alice, got %q", h.last.UserID)
	}
	if h.last.SessionID != roomID {
		t.Errorf("expected SessionID %q, got %q", roomID, h.last.SessionID)
	}
	if h.last.Text != "what did I spend on food?" {
		t.Errorf("unexpected Text: %q", h.last.Text)
	}
}

func TestPerRoomSerialization(t *testing.T) {
	m := newTestMatrix(testConfig())

	var order []int
	var mu sync.Mutex

	slow := func(_ context.Context, _ channel.Message) (channel.Response, error) {
		mu.Lock()
		order = append(order, 1)
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		order = append(order, 2)
		mu.Unlock()
		return channel.Response{Text: "ok"}, nil
	}

	roomID := "!shared:example.com"
	evt1 := buildEvent("@alice:example.com", roomID, "msg1")
	evt2 := buildEvent("@partner:example.com", roomID, "msg2")

	var wg sync.WaitGroup
	wg.Go(func() { m.handleMessage(t.Context(), evt1, slow) })
	wg.Go(func() { m.handleMessage(t.Context(), evt2, slow) })
	wg.Wait()

	// Serialised: pattern must be [1,2,1,2] — one message finishes before the next starts.
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 4 {
		t.Fatalf("expected 4 order entries, got %d: %v", len(order), order)
	}
	if order[0] == 1 && order[1] != 2 {
		t.Errorf("handler calls interleaved — per-room serialization broken: %v", order)
	}
}

func TestPlainReply(t *testing.T) {
	m := newTestMatrix(testConfig())
	resp := channel.Response{Text: "hello", Markdown: false}

	content, err := m.buildContent(resp)
	if err != nil {
		t.Fatalf("buildContent: %v", err)
	}
	if content.Format != "" {
		t.Errorf("expected no format for plain reply, got %q", content.Format)
	}
	if content.FormattedBody != "" {
		t.Errorf("expected no FormattedBody for plain reply, got %q", content.FormattedBody)
	}
	if content.Body != "hello" {
		t.Errorf("unexpected Body: %q", content.Body)
	}
}

func TestMarkdownReply(t *testing.T) {
	m := newTestMatrix(testConfig())
	resp := channel.Response{Text: "**bold** and _italic_", Markdown: true}

	content, err := m.buildContent(resp)
	if err != nil {
		t.Fatalf("buildContent: %v", err)
	}
	if content.Format != event.FormatHTML {
		t.Errorf("expected FormatHTML, got %q", content.Format)
	}
	if content.FormattedBody == "" {
		t.Error("expected non-empty FormattedBody for markdown reply")
	}
	if content.Body != resp.Text {
		t.Errorf("expected Body to be original markdown text, got %q", content.Body)
	}
}
