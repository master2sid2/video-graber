package auth

import (
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := CheckPassword("correct horse", h); err != nil || !ok {
		t.Fatalf("valid password rejected: %v", err)
	}
	if ok, _ := CheckPassword("wrong", h); ok {
		t.Fatal("wrong password accepted")
	}
	if _, err := CheckPassword("x", "garbage"); err == nil {
		t.Fatal("malformed hash accepted")
	}
	h2, _ := HashPassword("correct horse")
	if h == h2 {
		t.Fatal("hashes must be salted")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(2, time.Minute)
	got := []bool{l.Allow("a"), l.Allow("a"), l.Allow("a")}
	if !got[0] || !got[1] || got[2] {
		t.Fatalf("limiter should allow exactly 2 attempts, got %v", got)
	}
	if !l.Allow("b") {
		t.Fatal("keys must be independent")
	}
}

func TestValidateUsername(t *testing.T) {
	for _, ok := range []string{"bob", "alice_1", "a.b-c"} {
		if err := ValidateUsername(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"ab", "with space", "<script>", "этоимя"} {
		if ValidateUsername(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
