package api

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

type capturedEvent struct {
	event   string
	payload map[string]interface{}
}

type capturingNotifier struct {
	events chan capturedEvent
}

func (c *capturingNotifier) Send(_ context.Context, eventType string, payload map[string]interface{}) {
	c.events <- capturedEvent{event: eventType, payload: payload}
}

// TestRequestsCreate_SendsSanitisedRequestCreated is security review item S3:
// a new request fires requestCreated, and the text in its payload has no
// control or invisible characters, is capped, and cannot mention a channel,
// even when the provider's title and the username try.
func TestRequestsCreate_SendsSanitisedRequestCreated(t *testing.T) {
	stub := wellsRequestStub()
	hostile := "@everyone <!channel> [Free\u0007 nitro](https://evil.example)\u202E\u200B " + strings.Repeat("x", 400)
	stub.getBookByID["OL-HOSTILE"] = &models.Book{ForeignID: "OL-HOSTILE", Title: hostile,
		Author: &models.Author{ForeignID: "OL1A", Name: "@here\u0000Someone"}}
	f := newRequestsFixture(t, stub, &fakeAdder{})
	capture := &capturingNotifier{events: make(chan capturedEvent, 4)}
	f.h.WithNotifier(capture)
	if _, err := f.database.ExecContext(context.Background(), "UPDATE users SET username = ? WHERE id = ?", "@channel\u0007boss", f.requester.ID); err != nil {
		t.Fatal(err)
	}

	if rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL-HOSTILE"}`); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var got capturedEvent
	select {
	case got = <-capture.events:
	case <-time.After(5 * time.Second):
		t.Fatal("no requestCreated event sent")
	}
	if got.event != notifier.EventRequestCreated {
		t.Fatalf("event = %q, want %q", got.event, notifier.EventRequestCreated)
	}
	for _, key := range []string{"title", "author", "username"} {
		v, _ := got.payload[key].(string)
		if v == "" {
			t.Errorf("payload %s is empty", key)
		}
		for _, markup := range []string{"@", "<", ">", "[", "]"} {
			if strings.Contains(v, markup) {
				t.Errorf("payload %s = %q still carries %q", key, v, markup)
			}
		}
		for _, r := range v {
			if r < 0x20 || r == 0x7f || r == '\u202E' || r == '\u200B' {
				t.Errorf("payload %s = %q carries %U", key, v, r)
			}
		}
	}
	if n := utf8.RuneCountInString(got.payload["title"].(string)); n > requestTitleMaxRunes {
		t.Errorf("title is %d runes, cap is %d", n, requestTitleMaxRunes)
	}
	if got.payload["kind"] != "book" || got.payload["requestId"] == nil {
		t.Errorf("payload = %+v, want kind and requestId", got.payload)
	}

	// A refused request sends nothing.
	if rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL-HOSTILE"}`); rec.Code != 409 {
		t.Fatalf("repeat: %d", rec.Code)
	}
	select {
	case extra := <-capture.events:
		t.Fatalf("a refused request sent %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
}
