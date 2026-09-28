package maxsdk

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

const maxPhotoBytes = 10 << 20

var (
	ErrPhotoSource      = errors.New("photo source is not an approved HTTPS host")
	ErrPhotoUnavailable = errors.New("photo source is unavailable")
	ErrPhotoTooLarge    = errors.New("photo exceeds the 10 MiB limit")
	ErrPhotoFormat      = errors.New("photo must be a valid JPEG, PNG or WebP image")
)

type DownloadedPhoto struct {
	ContentType string
	Bytes       []byte
}

// PhotoDownloader accepts only exact, configured CDN hostnames. Its production
// transport resolves the hostname itself and connects only to public IPs, so a
// DNS change cannot turn an approved hostname into an internal network request.
type PhotoDownloader struct {
	hosts  map[string]bool
	client *http.Client
}

func NewPhotoDownloader(hosts []string) (*PhotoDownloader, error) {
	allowed := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		if host == "" || strings.TrimSpace(host) != host || strings.ToLower(host) != host || strings.ContainsAny(host, ":/@?# \\[]") || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
			return nil, ErrPhotoSource
		}
		allowed[host] = true
	}
	transport := &http.Transport{
		Proxy:                 nil,
		MaxIdleConns:          2,
		ResponseHeaderTimeout: 8 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil || port != "443" || !allowed[host] {
				return nil, ErrPhotoSource
			}
			resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, ErrPhotoUnavailable
			}
			for _, ip := range resolved {
				if publicMediaIP(ip) {
					return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				}
			}
			return nil, ErrPhotoSource
		},
	}
	return &PhotoDownloader{hosts: allowed, client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrPhotoSource }}}, nil
}

func publicMediaIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "fc00::/7"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return true
}

func (d *PhotoDownloader) Download(ctx context.Context, source string) (DownloadedPhoto, error) {
	if d == nil || d.client == nil || len(source) == 0 || len(source) > 500 {
		return DownloadedPhoto{}, ErrPhotoSource
	}
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Port() != "" && u.Port() != "443" || u.User != nil || u.Fragment != "" || !d.hosts[u.Hostname()] {
		return DownloadedPhoto{}, ErrPhotoSource
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return DownloadedPhoto{}, ErrPhotoSource
	}
	res, err := d.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return DownloadedPhoto{}, ctx.Err()
		}
		return DownloadedPhoto{}, ErrPhotoUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return DownloadedPhoto{}, ErrPhotoUnavailable
	}
	if res.ContentLength > maxPhotoBytes {
		return DownloadedPhoto{}, ErrPhotoTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxPhotoBytes+1))
	if err != nil {
		return DownloadedPhoto{}, ErrPhotoUnavailable
	}
	if len(data) > maxPhotoBytes {
		return DownloadedPhoto{}, ErrPhotoTooLarge
	}
	contentType := http.DetectContentType(data)
	formats := map[string]string{"image/jpeg": "jpeg", "image/png": "png", "image/webp": "webp"}
	if len(data) == 0 || formats[contentType] == "" {
		return DownloadedPhoto{}, ErrPhotoFormat
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != formats[contentType] || config.Width < 1 || config.Height < 1 || config.Width > 7680 || config.Height > 7680 {
		return DownloadedPhoto{}, ErrPhotoFormat
	}
	return DownloadedPhoto{ContentType: contentType, Bytes: data}, nil
}
