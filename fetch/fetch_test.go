//go:build !tinygo

package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("hello"))
		case "/204":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	ctx := context.Background()

	b, err := Fetch(ctx, srv.URL+"/ok")
	if err != nil {
		t.Fatalf("Fetch /ok: %v", err)
	}
	if string(b) != "hello" {
		t.Fatalf("Fetch /ok = %q, want %q", b, "hello")
	}

	b, err = Fetch(ctx, srv.URL+"/204")
	if err != nil {
		t.Fatalf("Fetch /204: %v", err)
	}
	if len(b) != 0 {
		t.Fatalf("Fetch /204 = %q, want empty", b)
	}

	if _, err := Fetch(ctx, srv.URL+"/bad"); err == nil {
		t.Fatal("Fetch on a 500 response should error")
	}
}

func TestFetchBadURL(t *testing.T) {
	if _, err := Fetch(context.Background(), "http://127.0.0.1:0/nope"); err == nil {
		t.Fatal("Fetch on an unreachable URL should error")
	}
}
