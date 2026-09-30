package maxsdk

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"errors"
	"net/http"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
)

// MAX API требует корневой сертификат Минцифры. Доверие ограничено клиентом
// MAX API и не меняет системное хранилище сертификатов.
// Источник: http://reestr-pki.ru/cdp/rootca_ssl_rsa2022.crt
// Отпечаток SHA-1: 8FF915CCAB7BC16F8C5C8099D53E0E115B3AEC2F
//
//go:embed certs/russian-trusted-root-ca.crt
var russianTrustedRootPEM []byte

// New pins the official MAX SDK constructor used by the future transport.
// The token is never logged or accepted from request data.
func New(token string) (*maxbot.Api, error) {
	if token == "" {
		return nil, errors.New("MAX token is empty")
	}
	client, err := newHTTPClient()
	if err != nil {
		return nil, err
	}
	return maxbot.NewApi(token, maxbot.WithHTTPClient(client))
}

func newHTTPClient() (*http.Client, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, errors.New("MAX API system certificate pool is unavailable")
	}
	if roots == nil {
		return nil, errors.New("MAX API system certificate pool is empty")
	}
	if ok := roots.AppendCertsFromPEM(russianTrustedRootPEM); !ok {
		return nil, errors.New("MAX API Russian Trusted Root CA is invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}, nil
}
