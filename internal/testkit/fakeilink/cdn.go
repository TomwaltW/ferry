// The media handling in this file — AES-128-ECB with PKCS#7, the aes_key
// encodings and the x-encrypted-param upload handshake — mirrors
// openilink-sdk-go v0.6.0 cdn.go, which is MIT licensed:
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

package fakeilink

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/TomwaltW/ferry/internal/ids"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
)

// cdnPrefix is where the fake CDN lives on the same httptest server as the bot
// API, so one Server stands in for both hosts.
const cdnPrefix = "/cdn"

// uploadParamHeader carries the download parameter of a freshly stored object.
const uploadParamHeader = "x-encrypted-param"

// SentFile is one outbound media message, already decrypted, so that a test
// can compare it with the bytes it handed to Ferry.
type SentFile struct {
	ToUserID, Name string
	Kind           ilinkwire.ItemType
	Plain          []byte
}

// cdnObject is one stored media object and the key that opens it.
type cdnObject struct {
	ciphertext []byte
	keyHex     string
}

// CDNBaseURL is what the wechat.cdn_base_url setting has to point at for
// uploads and downloads to reach this server.
func (s *Server) CDNBaseURL() string { return s.URL + cdnPrefix }

// PushImage queues an inbound image. The plaintext is encrypted under a fresh
// key and parked on the fake CDN, exactly as the gateway would present it.
func (s *Server) PushImage(fromUserID string, plain []byte) (contextToken string) {
	media, ciphertext := s.store(plain)
	return s.PushMessage(ilinkwire.Message{
		FromUserID: fromUserID,
		ItemList: []ilinkwire.MessageItem{{
			Type: ilinkwire.ItemImage,
			ImageItem: &ilinkwire.ImageItem{
				Media:   media,
				URL:     "https://cdn.example/image.jpg",
				MidSize: int64(len(ciphertext)),
			},
		}},
	})
}

// PushFile queues an inbound file attachment under the name given.
func (s *Server) PushFile(fromUserID, name string, plain []byte) (contextToken string) {
	media, _ := s.store(plain)
	return s.PushMessage(ilinkwire.Message{
		FromUserID: fromUserID,
		ItemList: []ilinkwire.MessageItem{{
			Type: ilinkwire.ItemFile,
			FileItem: &ilinkwire.FileItem{
				Media:    media,
				FileName: name,
				MD5:      fmt.Sprintf("%x", md5.Sum(plain)),
				Len:      strconv.Itoa(len(plain)),
			},
		}},
	})
}

// SentFiles returns every outbound image or file the gateway accepted, with
// the bytes decrypted. Media the gateway swallowed on purpose is included, the
// same way Sent reports it.
func (s *Server) SentFiles() []SentFile {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []SentFile
	for _, rec := range s.sent {
		for _, it := range rec.Items {
			var media *ilinkwire.CDNMedia
			var name string
			switch {
			case it.Type == ilinkwire.ItemImage && it.ImageItem != nil:
				media = it.ImageItem.Media
			case it.Type == ilinkwire.ItemFile && it.FileItem != nil:
				media, name = it.FileItem.Media, it.FileItem.FileName
			default:
				continue
			}
			if media == nil {
				continue
			}
			obj, ok := s.objects[media.EncryptQueryParam]
			if !ok {
				continue
			}
			plain, err := decryptHexKey(obj.ciphertext, obj.keyHex)
			if err != nil {
				// A test asserting on the bytes is better served by an empty
				// entry than by a panic inside the double.
				plain = nil
			}
			out = append(out, SentFile{
				ToUserID: rec.ToUserID,
				Name:     name,
				Kind:     it.Type,
				Plain:    plain,
			})
		}
	}
	return out
}

// store encrypts plain under a fresh key, parks it on the CDN and returns the
// reference a message carries to it.
func (s *Server) store(plain []byte) (*ilinkwire.CDNMedia, []byte) {
	key := make([]byte, aes.BlockSize)
	_, _ = rand.Read(key)
	keyHex := hex.EncodeToString(key)
	ciphertext := encryptECB(plain, key)
	param := ids.New("cdn")

	s.mu.Lock()
	s.objects[param] = &cdnObject{ciphertext: ciphertext, keyHex: keyHex}
	s.mu.Unlock()

	return &ilinkwire.CDNMedia{
		EncryptQueryParam: param,
		// base64 of the hexadecimal key, the encoding the real gateway uses
		// for files and voice messages.
		AESKey:      base64.StdEncoding.EncodeToString([]byte(keyHex)),
		EncryptType: ilinkwire.EncryptAES128ECB,
	}, ciphertext
}

// handleGetUploadURL hands out an upload parameter and remembers the key the
// client says it will encrypt with, so that SentFiles can decrypt later. Only
// upload_param is returned: the client has to build the URL from cdn_base_url,
// which is the path a deployment without upload_full_url takes.
func (s *Server) handleGetUploadURL(w http.ResponseWriter, r *http.Request) {
	var req ilinkwire.GetUploadURLReq
	if err := decode(r, &req); err != nil || req.FileKey == "" || req.AESKey == "" {
		writeRaw(w, http.StatusOK, bodyBadRequest)
		return
	}

	s.mu.Lock()
	s.uploadKeys[req.FileKey] = req.AESKey
	s.mu.Unlock()

	writeJSON(w, ilinkwire.GetUploadURLResp{UploadParam: "up-" + req.FileKey})
}

// handleCDNUpload stores the ciphertext and names it in the response header,
// which is where the client reads the parameter its message must carry.
func (s *Server) handleCDNUpload(w http.ResponseWriter, r *http.Request) {
	filekey := r.URL.Query().Get("filekey")
	if filekey == "" || r.URL.Query().Get("encrypted_query_param") == "" {
		http.Error(w, "missing filekey or encrypted_query_param", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	keyHex, known := s.uploadKeys[filekey]
	if known {
		s.objects["dl-"+filekey] = &cdnObject{ciphertext: body, keyHex: keyHex}
	}
	s.mu.Unlock()

	if !known {
		http.Error(w, "unknown filekey", http.StatusForbidden)
		return
	}
	w.Header().Set(uploadParamHeader, "dl-"+filekey)
	w.WriteHeader(http.StatusOK)
}

// handleCDNDownload serves a stored object as it is, still encrypted.
func (s *Server) handleCDNDownload(w http.ResponseWriter, r *http.Request) {
	param := r.URL.Query().Get("encrypted_query_param")

	s.mu.Lock()
	obj, ok := s.objects[param]
	s.mu.Unlock()

	if !ok {
		http.Error(w, "unknown object", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(obj.ciphertext)
}

// encryptECB is AES-128-ECB with PKCS#7 padding.
func encryptECB(plain, key []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic("fakeilink: " + err.Error()) // only a wrong key length gets here
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)

	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}
	return out
}

// decryptHexKey reverses encryptECB with a key given in hexadecimal, which is
// how getuploadurl receives it.
func decryptHexKey(ciphertext []byte, keyHex string) ([]byte, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("fakeilink: aes key is not hexadecimal: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("fakeilink: aes cipher: %w", err)
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("fakeilink: ciphertext is %d bytes", len(ciphertext))
	}

	out := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}
	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(out) {
		return nil, fmt.Errorf("fakeilink: invalid padding %d", pad)
	}
	return out[:len(out)-pad], nil
}
