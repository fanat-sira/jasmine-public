package telegram

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("request failed at %s", r.URL.String())
}
func TestPhotoSelectionWithinLimit(t *testing.T) {
	photos := []PhotoSize{{FileID: "small", Width: 100, Height: 100, FileSize: 1000}, {FileID: "large", Width: 2000, Height: 2000, FileSize: 20 << 20}, {FileID: "usable", Width: 1000, Height: 1000, FileSize: 5 << 20}}
	best := BestPhotoWithin(photos, 10<<20)
	if best == nil || best.FileID != "usable" {
		t.Fatalf("unexpected photo: %+v", best)
	}
}
func TestTelegramNetworkErrorsRedactBotToken(t *testing.T) {
	c := NewClientWithBaseURL("secret-token", "https://telegram.test", &http.Client{Transport: failingTransport{}})
	_, err := c.GetFile(context.Background(), "photo")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("getFile exposed token: %v", err)
	}
	_, err = c.DownloadFile(context.Background(), &File{FilePath: "a.jpg"}, 1024)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("download exposed token: %v", err)
	}
}
