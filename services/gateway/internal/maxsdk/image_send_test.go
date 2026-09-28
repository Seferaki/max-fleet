package maxsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

type stubImageUploader struct {
	calls int
	token string
	err   error
}

func (s *stubImageUploader) Upload(_ context.Context, kind model.UploadType, reader io.Reader, name string, size int64) (string, error) {
	s.calls++
	data, err := io.ReadAll(reader)
	if err != nil || kind != model.UploadImage || name != "photo.png" || int64(len(data)) != size {
		return "", errors.New("invalid image upload")
	}
	return s.token, s.err
}

func TestSDKTransportSendsAuthorizedImageByToken(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		var body struct {
			Text        string `json:"text"`
			Attachments []struct {
				Type    string `json:"type"`
				Payload struct {
					Token string `json:"token"`
				} `json:"payload"`
			} `json:"attachments"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/messages" || r.URL.Query().Get("user_id") != "123" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Text != "До: левый борт" || len(body.Attachments) != 1 || body.Attachments[0].Type != "image" || body.Attachments[0].Payload.Token != "synthetic-image-token" {
			t.Error("wrong MAX image request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"body":{"mid":"max-image-1"}}}`))
	}))
	defer server.Close()
	api, err := maxbot.NewApi("synthetic-bot-token", maxbot.WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	uploader := &stubImageUploader{token: "synthetic-image-token"}
	api.Upload = uploader
	transport, err := NewTransport(api)
	if err != nil {
		t.Fatal(err)
	}
	id, err := transport.SendImage(context.Background(), 123, "До: левый борт", "image/png", encoded.Bytes())
	if err != nil || id != "max-image-1" || sends != 1 || uploader.calls != 1 {
		t.Fatalf("image send = %q err=%v sends=%d uploads=%d", id, err, sends, uploader.calls)
	}
	if _, err := transport.SendImage(context.Background(), 123, "До: левый борт", "image/jpeg", encoded.Bytes()); !errors.Is(err, ErrPhotoFormat) || uploader.calls != 1 {
		t.Fatalf("mismatched bytes accepted: %v", err)
	}
	uploader.token = ""
	if _, err := transport.SendImage(context.Background(), 123, "До: левый борт", "image/png", encoded.Bytes()); err == nil || sends != 1 {
		t.Fatalf("empty token sent: %v", err)
	}
}
