package dockerapi

import "testing"

func TestNewRejectsUnknownScheme(t *testing.T) {
	if _, err := New("ftp://nope"); err == nil {
		t.Fatal("expected an error for an unsupported scheme")
	}
}

func TestNewAcceptsTCP(t *testing.T) {
	c, err := New("tcp://127.0.0.1:2375")
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "http://127.0.0.1:2375" {
		t.Fatalf("baseURL = %s", c.baseURL)
	}
}

func TestContainerName(t *testing.T) {
	if got := (Container{ID: "abcdef123456789", Names: []string{"/web"}}).Name(); got != "web" {
		t.Fatalf("name = %q", got)
	}
	if got := (Container{ID: "abcdef123456789"}).Name(); got != "abcdef123456" {
		t.Fatalf("fallback name = %q", got)
	}
}
