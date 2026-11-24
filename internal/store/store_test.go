package store

import (
	"errors"
	"testing"
	"time"
)

func TestStoreFlow(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()

	admin, err := s.CreateUser(ctx, "Admin", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "admin", "hash", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("usernames must be case-insensitive unique, got %v", err)
	}

	if err := s.CreateSession(ctx, "tok", admin.ID, time.Hour, "1.2.3.4", "ua"); err != nil {
		t.Fatal(err)
	}
	if u, err := s.SessionUser(ctx, "tok"); err != nil || u.ID != admin.ID {
		t.Fatalf("session lookup: %v", err)
	}

	inv, err := s.CreateInvite(ctx, "code", "note", admin.ID, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterWithInvite(ctx, inv.Code, "bob", "h"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterWithInvite(ctx, inv.Code, "eve", "h"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("used invite must be rejected, got %v", err)
	}

	cat, err := s.CreateCategory(ctx, admin.ID, "Anime", "#ff4a26")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AddHistory(ctx, admin.ID, &HistoryItem{PageURL: "https://x", Title: "50% off_sale", CategoryID: &cat.ID, Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishHistory(ctx, id, "done", "", 123); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.ListHistory(ctx, admin.ID, HistoryQuery{Search: "50%"})
	if err != nil || total != 1 || items[0].Size != 123 || *items[0].CategoryID != cat.ID {
		t.Fatalf("history: %v %d %+v", err, total, items)
	}
	if _, total, _ := s.ListHistory(ctx, admin.ID, HistoryQuery{Search: "5_%"}); total != 0 {
		t.Fatal("LIKE wildcards in the search must be escaped")
	}
	if err := s.DeleteCategory(ctx, admin.ID, cat.ID); err != nil {
		t.Fatal(err)
	}
	items, _, _ = s.ListHistory(ctx, admin.ID, HistoryQuery{CategoryID: -1})
	if len(items) != 1 {
		t.Fatal("deleting a category must keep its items uncategorized")
	}
}
