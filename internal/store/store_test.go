package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "kai.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestProviders(t *testing.T) {
	s := open(t)
	p, err := s.AddProvider(Provider{Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", APIKey: "sk-x"})
	if err != nil || p.ID == 0 {
		t.Fatalf("add: %v %+v", err, p)
	}
	if _, err := s.AddProvider(Provider{Name: "openrouter", BaseURL: "https://x"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate (names are case-insensitive), got %v", err)
	}
	if err := s.SetProviderModel(p.ID, "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProviderByID(p.ID)
	if err != nil || got.Model != "gpt-4o-mini" || got.APIKey != "sk-x" {
		t.Fatalf("get: %v %+v", err, got)
	}
	list, _ := s.Providers()
	if len(list) != 1 {
		t.Fatalf("list: %d", len(list))
	}
	if err := s.DeleteProvider(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProviderByID(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSessionsAndMessages(t *testing.T) {
	s := open(t)
	now := time.Now()
	for _, id := range []string{"a", "b"} {
		if err := s.CreateSession(Session{ID: id, Title: "t-" + id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.AppendMessage("a", "user", "hello", now)
	_ = s.AppendMessage("a", "assistant", "hi", now)
	time.Sleep(5 * time.Millisecond)
	_ = s.AppendMessage("b", "user", "later", now) // b is now the most recent

	list, err := s.Sessions(0)
	if err != nil || len(list) != 2 || list[0].ID != "b" || list[1].Messages != 2 {
		t.Fatalf("sessions: %v %+v", err, list)
	}
	msgs, _ := s.Messages("a")
	if len(msgs) != 2 || msgs[0].Text != "hello" || msgs[1].Role != "assistant" {
		t.Fatalf("messages: %+v", msgs)
	}
	if err := s.RenameSession("a", "  Renamed "); err != nil {
		t.Fatal(err)
	}
	if x, _ := s.Session("a"); x.Title != "Renamed" {
		t.Fatalf("rename: %q", x.Title)
	}
	if err := s.DeleteSession("a"); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := s.Messages("a"); len(msgs) != 0 {
		t.Fatal("messages must go with the session (ON DELETE CASCADE)")
	}
}

func TestSettingsAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kai.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Setting("active_provider"); ok {
		t.Fatal("setting should start unset")
	}
	_ = s.SetSetting("active_provider", "custom:1")
	_ = s.SetSetting("active_provider", "web")
	_ = s.Close()

	s, err = Open(path) // migrations must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, ok := s.Setting("active_provider"); !ok || v != "web" {
		t.Fatalf("setting after reopen: %q %v", v, ok)
	}
}
