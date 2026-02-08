package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/lib/rest"
)

const (
	// Can be scraped from the js asset
	// https://static.gopro.com/web-apps/assets/login/f52b4689338c803186ba4415d64b04814fae7c85/_next/static/chunks/pages/index-4f91eade1abfab95.js.
	// Check browser network logs if the public key changes.
	pemPublicKey = `
-----BEGIN PUBLIC KEY-----
MIGeMA0GCSqGSIb3DQEBAQUAA4GMADCBiAKBgEOtpzitZ8Yedx1C8lWu0BV9lOZh
j1ZmDQySIR3A7qi3iH3K1NAt8D34e8IxI6hii0FXNIh64JWZ3/E3yPpYziPphtDE
lRclRudndBfnx+Rf4GPQaJgrl8YgwhDP7Ck2klVdftymehZw8AHnwSdzQUIUVDZe
9TeXqn4yy2rABN9JAgMBAAE=
-----END PUBLIC KEY-----`
	loginAPIURL      = "https://gopro.com/login/api/login/"
	validateEndpoint = "https://gopro.com/media-library/"
)

var (
	defaultHeaders = map[string]string{
		"Accept":          "application/vnd.gopro.jk.media+json; version=2.0.0",
		"Accept-Language": "en-US,en;q=0.9,bg;q=0.8,es;q=0.7",
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	}
)

var (
	ErrorMustAuthenticate = errors.New("could not validate session, must authenticate")
)

// Session represents an iCloud session
type Session struct {
	srv *rest.Client
	jar http.CookieJar
}

// AuthWithToken authenticates the session
func (s *Session) authenticate(ctx context.Context, email, password string) error {
	encryptedPassword, err := encryptWithPublicKey(password)
	if err != nil {
		return fmt.Errorf("failed to encrypt password: %w", err)
	}

	payload := map[string]string{
		"email":        email,
		"password":     encryptedPassword,
		"redirectUri":  "https://gopro.com/en/us/",
		"redirect_uri": "https://gopro.com/en/us/",
	}

	resp, err := s.srv.CallJSON(ctx, &rest.Opts{Method: "POST", RootURL: loginAPIURL}, payload, nil)
	if err != nil {
		return fmt.Errorf("failed to make POST request to login API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("authentication failed, status code: %d, response: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// ValidateSession validates the session
func (s *Session) validateSession(ctx context.Context) error {
	// TODO: there is a bug on the initial login where the URL will redirect to /login with
	// 200. We should check for cookies first I think
	resp, err := s.srv.Call(ctx, &rest.Opts{Method: http.MethodGet, RootURL: validateEndpoint, IgnoreStatus: true})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrorMustAuthenticate
	}

	return nil
}

// GetCookiesForDomain filters the provided cookies based on the domain of the given URL.
func (s *Session) GetCookieString() string {
	cookieHeader := ""
	// we only care about name and value.
	for _, cookie := range s.jar.Cookies(baseCookieURL) {
		cookieHeader = cookieHeader + cookie.Name + "=" + cookie.Value + ";"
	}
	return cookieHeader
}

// NewSession creates a new Session instance with default values.
func NewSession(jar http.CookieJar) *Session {
	// Don't duplicate this code with client.go
	httpClient := fshttp.NewClient(context.Background())
	httpClient.Jar = jar
	srv := rest.NewClient(httpClient)

	for k, v := range defaultHeaders {
		srv.SetHeader(k, v)
	}
	session := &Session{srv: srv, jar: jar}
	return session
}

// getPublicKey fetches and caches the RSA public key.
func getPublicKey() (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemPublicKey))
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("failed to parse PEM block containing public key")
	}

	pub, errRes := x509.ParsePKIXPublicKey(block.Bytes)
	if errRes != nil {
		return nil, fmt.Errorf("failed to parse DER encoded public key: %w", errRes)
	}

	publicKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("parsed public key is not an RSA public key")
	}

	return publicKey, nil
}

// encryptWithPublicKey encrypts data using the RSA public key.
func encryptWithPublicKey(data string) (string, error) {
	pubKey, err := getPublicKey()
	if err != nil {
		return "", fmt.Errorf("failed to get public key for encryption: %w", err)
	}

	encryptedBytes, err := rsa.EncryptPKCS1v15(rand.Reader, pubKey, []byte(data))
	if err != nil {
		return "", fmt.Errorf("failed to encrypt data: %w", err)
	}

	return base64.StdEncoding.EncodeToString(encryptedBytes), nil
}
