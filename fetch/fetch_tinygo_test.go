//go:build tinygo

package fetch

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
)

// TestFetchBinaryBody guards the fix for the release-build corruption: the
// TinyGo path must copy the body as bytes, not decode it with Response.text()
// (which mangles anything that is not valid UTF-8, e.g. images and AES-GCM
// ciphertext). A `data:` URL exercises the same ArrayBuffer path as a network
// fetch without needing a server.
func TestFetchBinaryBody(t *testing.T) {
	want := make([]byte, 256)
	for i := range want {
		want[i] = byte(i)
	}
	url := "data:application/octet-stream;base64," + base64.StdEncoding.EncodeToString(want)

	got, err := Fetch(context.Background(), url)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Fetch body corrupted: got %d bytes, want %d", len(got), len(want))
	}
}
