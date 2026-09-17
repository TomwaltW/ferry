// The CDN behaviour in this file — AES-128-ECB with PKCS#7, the two aes_key
// encodings, the download and upload URL shapes, and the x-encrypted-param
// upload handshake — is adapted from openilink-sdk-go v0.6.0 cdn.go, which is
// MIT licensed:
//
//	MIT License
//
//	Copyright (c) 2026 The OpenILink Authors
//
//	Permission is hereby granted, free of charge, to any person obtaining a copy
//	of this software and associated documentation files (the "Software"), to deal
//	in the Software without restriction, including without limitation the rights
//	to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
//	copies of the Software, and to permit persons to whom the Software is
//	furnished to do so, subject to the following conditions:
//
//	The above copyright notice and this permission notice shall be included in all
//	copies or substantial portions of the Software.
//
//	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
//	IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
//	FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
//	AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
//	LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
//	OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
//	SOFTWARE.

package wechat

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TomwaltW/ferry/internal/ilinkwire"
)

const (
	// defaultCDNBaseURL is openilink-sdk-go's production media endpoint. It is
	// not part of the frozen ilinkwire contract, so it lives here; tests and
	// deployments override it through wechat.cdn_base_url.
	defaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"

	// cdnTimeout bounds one CDN request. Like the long poll deadline this is
	// wall clock by nature; every wait Ferry decides on goes through the clock.
	cdnTimeout = 60 * time.Second

	// uploadParamHeader carries the download parameter of a freshly uploaded
	// object, which is what a message has to reference.
	uploadParamHeader = "x-encrypted-param"

	aesKeyLen = 16 // AES-128
)

// errMediaTooLarge is the body outgrowing max_media_mb, which is not a failure
// the user should be told to retry.
var errMediaTooLarge = errors.New("wechat: media exceeds max_media_mb")

// cdnClient talks to the media CDN. It carries none of the bot credentials:
// the encrypted query parameter is the only capability a CDN request needs.
type cdnClient struct {
	http *http.Client
	base string // no trailing slash
}

func newCDNClient(base string) *cdnClient {
	if base == "" {
		base = defaultCDNBaseURL
	}
	return &cdnClient{http: &http.Client{}, base: strings.TrimRight(base, "/")}
}

// downloadURL resolves where a media reference is fetched from. full_url wins
// when the gateway sent one, because it may point at a different edge.
func (c *cdnClient) downloadURL(m *ilinkwire.CDNMedia) (string, error) {
	switch {
	case m == nil:
		return "", errors.New("wechat: media reference is nil")
	case m.FullURL != "":
		return m.FullURL, nil
	case m.EncryptQueryParam != "":
		return c.base + "/download?encrypted_query_param=" + url.QueryEscape(m.EncryptQueryParam), nil
	default:
		return "", errors.New("wechat: media has neither full_url nor encrypt_query_param")
	}
}

// uploadURL resolves where the ciphertext is POSTed to.
func (c *cdnClient) uploadURL(resp ilinkwire.GetUploadURLResp, filekey string) (string, error) {
	if resp.UploadFullURL != "" {
		return resp.UploadFullURL, nil
	}
	if resp.UploadParam == "" {
		return "", errors.New("wechat: getuploadurl returned no upload url")
	}
	return c.base + "/upload?encrypted_query_param=" + url.QueryEscape(resp.UploadParam) +
		"&filekey=" + url.QueryEscape(filekey), nil
}

// download fetches and decrypts one media reference. The key is passed in
// rather than read off the reference because an image item may carry it beside
// the reference instead of inside it. At most limit bytes of ciphertext are
// read; a larger body yields errMediaTooLarge without the whole file ever
// reaching memory.
func (c *cdnClient) download(ctx context.Context, m *ilinkwire.CDNMedia, aesKey string, limit int64) ([]byte, error) {
	key, err := parseAESKey(aesKey)
	if err != nil {
		return nil, err
	}
	target, err := c.downloadURL(m)
	if err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, cdnTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("wechat: cdn download: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wechat: cdn download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("wechat: cdn download: http %d", resp.StatusCode)
	}

	ciphertext, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("wechat: cdn download: %w", err)
	}
	if int64(len(ciphertext)) > limit {
		return nil, errMediaTooLarge
	}

	plain, err := decryptECB(ciphertext, key)
	if err != nil {
		return nil, fmt.Errorf("wechat: cdn decrypt: %w", err)
	}
	return plain, nil
}

// upload POSTs the ciphertext and returns the parameter a message uses to
// reference the object. It never retries: the outbox owns that decision.
func (c *cdnClient) upload(ctx context.Context, target string, ciphertext []byte) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, cdnTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(ciphertext))
	if err != nil {
		return "", fmt.Errorf("wechat: cdn upload: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("wechat: cdn upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		detail := resp.Header.Get("x-error-message")
		if detail == "" {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			detail = strings.TrimSpace(string(body))
		}
		return "", fmt.Errorf("wechat: cdn upload: http %d: %s", resp.StatusCode, detail)
	}
	_, _ = io.Copy(io.Discard, resp.Body)

	param := resp.Header.Get(uploadParamHeader)
	if param == "" {
		return "", fmt.Errorf("wechat: cdn upload: response carries no %s", uploadParamHeader)
	}
	return param, nil
}

// newAESKey draws a fresh AES-128 key.
func newAESKey() ([]byte, error) {
	key := make([]byte, aesKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("wechat: generate aes key: %w", err)
	}
	return key, nil
}

// newFileKey draws the 32 character hexadecimal key naming one CDN object.
func newFileKey() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("wechat: generate filekey: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// wireAESKey encodes a key the way a message item carries it: base64 of the
// hexadecimal form, which is the encoding the gateway sends for files and
// voice messages.
func wireAESKey(key []byte) string {
	return base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key)))
}

// parseAESKey decodes the aes_key of a media reference. Two encodings are seen
// in the wild, and the base64 alphabet varies too, so every variant is tried in
// the order openilink-sdk-go uses:
//
//	base64(16 raw bytes)  — images
//	base64(32 hex digits) — files, voice, video
func parseAESKey(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("wechat: media carries no aes_key")
	}
	decoded, err := decodeBase64Flexible(s)
	if err != nil {
		return nil, fmt.Errorf("wechat: decode aes_key: %w", err)
	}
	if len(decoded) == aesKeyLen {
		return decoded, nil
	}
	if len(decoded) == 2*aesKeyLen {
		if raw, err := hex.DecodeString(string(decoded)); err == nil {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("wechat: aes_key decodes to %d bytes, want %d raw or %d hexadecimal",
		len(decoded), aesKeyLen, 2*aesKeyLen)
}

// decodeBase64Flexible accepts both base64 alphabets, padded or not.
func decodeBase64Flexible(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("not base64: %q", s)
}

// encryptECB encrypts with AES-128-ECB and PKCS#7 padding. ECB is what the
// gateway speaks; the key is single use and never reaches a second object.
func encryptECB(plain, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("wechat: aes cipher: %w", err)
	}
	padded := pkcs7Pad(plain, aes.BlockSize)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}
	return out, nil
}

func decryptECB(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("wechat: aes cipher: %w", err)
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("wechat: ciphertext is %d bytes, not a whole number of blocks", len(ciphertext))
	}
	out := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}
	return pkcs7Unpad(out)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("wechat: pkcs7: empty plaintext")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(data) {
		return nil, fmt.Errorf("wechat: pkcs7: invalid padding %d", pad)
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, errors.New("wechat: pkcs7: inconsistent padding")
		}
	}
	return data[:len(data)-pad], nil
}

// paddedSize is the ciphertext size PKCS#7 produces, which getuploadurl wants
// before anything has been encrypted.
func paddedSize(n int64) int64 {
	return (n/aes.BlockSize + 1) * aes.BlockSize
}
