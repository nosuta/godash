//go:build !tinygo

package fetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
)

func Fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return nil, fmt.Errorf("fetch failed: %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Put sends a PUT request with a binary body and returns the response body. It
// is the upload counterpart to Fetch (a Blossom PUT /upload on every platform).
// headers are added verbatim (for example the Blossom `Authorization` token).
func Put(ctx context.Context, url, contentType string, body []byte, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("put failed: %d %s", resp.StatusCode, string(reason))
	}
	return io.ReadAll(resp.Body)
}
