package llm

import (
	"testing"
	"time"
)

func TestConversationCommitIsolationAndBounds(t *testing.T) {
	c := NewConversations()
	for i := 0; i < 5; i++ {
		_, finish, err := c.Begin("room-a", 2)
		if err != nil {
			t.Fatal(err)
		}
		finish("hello", "reply")
	}
	history, finish, err := c.Begin("room-a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 {
		t.Fatalf("history length %d", len(history))
	}
	if _, _, err := c.Begin("room-a", 2); err == nil {
		t.Fatal("concurrent room turn allowed")
	}
	if err := c.Clear("room-a"); err == nil {
		t.Fatal("cleared active turn")
	}
	other, done, err := c.Begin("room-b", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatal("room history leaked")
	}
	done("", "")
	finish("discarded", "")
	history, finish, err = c.Begin("room-a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 || history[3]["content"] != "reply" {
		t.Fatal("discarded turn entered history")
	}
	finish("", "")
	if err := c.Clear("room-a"); err != nil {
		t.Fatal(err)
	}
	history, finish, err = c.Begin("room-a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatal("clear failed")
	}
	finish("", "")
}

func TestConversationExpiryAndCapacity(t *testing.T) {
	c := NewConversations()
	c.rooms["expired"] = &room{last: time.Now().Add(-time.Hour), history: []map[string]string{{"content": "old"}}}
	h, finish, err := c.Begin("expired", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 0 {
		t.Fatal("expired context retained")
	}
	finish("", "")
	for i := 0; i < 140; i++ {
		key := string(rune(i + 100))
		_, done, err := c.Begin(key, 2)
		if err != nil {
			t.Fatal(err)
		}
		done("a", "b")
	}
	if len(c.rooms) > 128 {
		t.Fatal("unbounded sessions")
	}
}
