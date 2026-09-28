package maxsdk

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

type photoRoundTrip func(*http.Request) (*http.Response, error)

func (f photoRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPhotoDownloaderSourceAndNetworkPolicy(t *testing.T) {
	d, err := NewPhotoDownloader([]string{"cdn.max.ru"})
	if err != nil {
		t.Fatal(err)
	}
	d.client.Transport = photoRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("unapproved photo source reached HTTP client")
		return nil, nil
	})
	for _, source := range []string{"https://cdn.max.ru.evil.example/photo", "http://cdn.max.ru/photo", "https://user@cdn.max.ru/photo", "https://cdn.max.ru:444/photo", "https://127.0.0.1/photo", "opaque-token", "https://cdn.max.ru/photo#fragment"} {
		if _, err := d.Download(context.Background(), source); !errors.Is(err, ErrPhotoSource) {
			t.Fatalf("source %q: %v", source, err)
		}
	}
	for _, source := range []string{"localhost", "127.0.0.1", "cdn.max.ru:443", "CDN.MAX.RU"} {
		if _, err := NewPhotoDownloader([]string{source}); !errors.Is(err, ErrPhotoSource) {
			t.Fatalf("host %q: %v", source, err)
		}
	}
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.1", "::1", "fc00::1", "2001:db8::1"} {
		if publicMediaIP(netip.MustParseAddr(address)) {
			t.Fatalf("private or reserved IP %s accepted", address)
		}
	}
	if !publicMediaIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public IP rejected")
	}
}

func TestPhotoDownloaderValidatesBytesSizeAndRedirect(t *testing.T) {
	d, err := NewPhotoDownloader([]string{"cdn.max.ru"})
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	response := func(status int, data []byte, size int64) *http.Response {
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), ContentLength: size}
	}
	d.client.Transport = photoRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.URL.Host != "cdn.max.ru" {
			t.Fatal("photo download leaked auth or changed host")
		}
		return response(200, encoded.Bytes(), int64(encoded.Len())), nil
	})
	if got, err := d.Download(context.Background(), "https://cdn.max.ru/photo?private=opaque"); err != nil || got.ContentType != "image/png" || !bytes.Equal(got.Bytes, encoded.Bytes()) {
		t.Fatalf("valid image = %q bytes=%d err=%v", got.ContentType, len(got.Bytes), err)
	}
	d.client.Transport = photoRoundTrip(func(*http.Request) (*http.Response, error) { return response(200, []byte("not an image"), 12), nil })
	if _, err := d.Download(context.Background(), "https://cdn.max.ru/photo"); !errors.Is(err, ErrPhotoFormat) {
		t.Fatalf("invalid image = %v", err)
	}
	d.client.Transport = photoRoundTrip(func(*http.Request) (*http.Response, error) { return response(200, nil, maxPhotoBytes+1), nil })
	if _, err := d.Download(context.Background(), "https://cdn.max.ru/photo"); !errors.Is(err, ErrPhotoTooLarge) {
		t.Fatalf("declared oversized image = %v", err)
	}
	d.client.Transport = photoRoundTrip(func(*http.Request) (*http.Response, error) {
		return response(200, []byte(strings.Repeat("a", maxPhotoBytes+1)), -1), nil
	})
	if _, err := d.Download(context.Background(), "https://cdn.max.ru/photo"); !errors.Is(err, ErrPhotoTooLarge) {
		t.Fatalf("streamed oversized image = %v", err)
	}
	d.client.Transport = photoRoundTrip(func(*http.Request) (*http.Response, error) {
		r := response(302, nil, 0)
		r.Header.Set("Location", "https://evil.example/photo")
		return r, nil
	})
	if _, err := d.Download(context.Background(), "https://cdn.max.ru/photo"); !errors.Is(err, ErrPhotoUnavailable) {
		t.Fatalf("redirect = %v", err)
	}
}
