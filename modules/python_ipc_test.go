package modules

import (
	"testing"
	"time"
)

// TestPythonWebReplyRouting pins the dashboard command correlation: a reply
// carrying req_id is delivered to the waiting web caller instead of Discord,
// replies without req_id stay on the Discord path, and unknown req_ids are
// consumed (never double-delivered) without panicking on a nil logger.
func TestPythonWebReplyRouting(t *testing.T) {
	p := NewPythonIPC(nil, nil) // no real process — only the message handlers

	// Registered waiter receives a respond reply.
	reqID := "web-test-1"
	ch := make(chan map[string]interface{}, 4)
	p.pendingMu.Lock()
	p.pending[reqID] = ch
	p.pendingMu.Unlock()

	p.handleRespond(map[string]interface{}{"type": "respond", "req_id": reqID, "title": "T", "description": "D"})
	select {
	case resp := <-ch:
		if resp["title"] != "T" || resp["description"] != "D" {
			t.Errorf("waiter got %v", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never received the reply")
	}

	// reply_text and error replies route the same way.
	reqID2 := "web-test-2"
	ch2 := make(chan map[string]interface{}, 4)
	p.pendingMu.Lock()
	p.pending[reqID2] = ch2
	p.pendingMu.Unlock()
	p.handleReplyText(map[string]interface{}{"type": "reply_text", "req_id": reqID2, "text": "hi"})
	select {
	case resp := <-ch2:
		if resp["text"] != "hi" {
			t.Errorf("reply_text waiter got %v", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reply_text waiter never received")
	}

	reqID3 := "web-test-3"
	ch3 := make(chan map[string]interface{}, 4)
	p.pendingMu.Lock()
	p.pending[reqID3] = ch3
	p.pendingMu.Unlock()
	p.handleError(map[string]interface{}{"type": "error", "req_id": reqID3, "message": "boom"})
	select {
	case resp := <-ch3:
		if resp["type"] != "error" || resp["message"] != "boom" {
			t.Errorf("error waiter got %v", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error waiter never received")
	}

	// No req_id → Discord path (onRespond nil → silent no-op), must not panic.
	p.handleRespond(map[string]interface{}{"type": "respond", "title": "X", "description": "Y"})

	// Unknown req_id → consumed, must not panic with a nil logger.
	p.handleRespond(map[string]interface{}{"type": "respond", "req_id": "nope", "title": "Z"})

	// Note: entry cleanup happens in SendCommandFromWeb's deferred delete
	// (the only registration path); deliverWeb itself only fans out replies.
}

// TestSendCommandFromWebFailsFastWithNoProcess: without a live stdin the call
// must fail immediately, not hang.
func TestSendCommandFromWebFailsFastWithNoProcess(t *testing.T) {
	p := NewPythonIPC(nil, nil)
	if _, err := p.SendCommandFromWeb("hello", nil, "", "1"); err == nil {
		t.Fatal("expected error for nil stdin, got nil")
	}
}
