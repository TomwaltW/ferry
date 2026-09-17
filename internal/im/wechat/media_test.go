package wechat

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/testkit/fakeilink"
)

// mediaFixture is one adapter wired to a fake gateway, with the home directory
// exposed so that a test can look at what landed in the inbox.
type mediaFixture struct {
	a    *Adapter
	s    *fakeilink.Server
	home string
}

// newMediaFixture logs an adapter in against a fake gateway. An empty cdnBase
// points media at the gateway's own CDN; maxMB of zero takes the default.
func newMediaFixture(t *testing.T, cdnBase string, maxMB int) mediaFixture {
	t.Helper()
	s := loggedIn(t, fakeilink.Options{})
	if cdnBase == "" {
		cdnBase = s.CDNBaseURL()
	}
	home := t.TempDir()

	a, err := New(Options{
		Config: config.WeChatConfig{
			Enabled:    true,
			BaseURL:    s.URL,
			Render:     renderPlainMode,
			CDNBaseURL: cdnBase,
			MaxMediaMB: maxMB,
		},
		Creds: ports.Credentials{
			BotToken: s.BotToken(),
			BotID:    s.BotID(),
			OwnerID:  owner,
			BaseURL:  s.URL,
		},
		KV:    newMemKV(),
		Home:  home,
		Clock: clock.NewFake(epoch),
		Log:   discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return mediaFixture{a: a, s: s, home: home}
}

// inboxDir is where this fixture's downloads for peer end up.
func (f mediaFixture) inboxDir() string {
	return filepath.Join(f.home, "inbox", entryHash(f.a.endpoint(peer)))
}

// only returns the single attachment of an inbound message.
func only(t *testing.T, in fsp.Inbound) fsp.Attachment {
	t.Helper()
	if len(in.Attachments) != 1 {
		t.Fatalf("attachments = %+v, want exactly one", in.Attachments)
	}
	return in.Attachments[0]
}

// readFile reads a downloaded attachment.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

func TestInboundImageDownloaded(t *testing.T) {
	f := newMediaFixture(t, "", 0)
	plain := []byte("\x89PNG\r\n\x1a\nthe bytes of a picture")

	f.s.PushImage(peer, plain)
	in := start(t, f.a).recv(t)

	att := only(t, in)
	if att.Kind != "image" {
		t.Fatalf("kind = %q, want image", att.Kind)
	}
	if att.Path == "" {
		t.Fatal("Path is empty, want the decrypted image on disk")
	}
	if dir := filepath.Dir(att.Path); dir != f.inboxDir() {
		t.Errorf("saved under %s, want %s", dir, f.inboxDir())
	}
	// The message id prefixes the name, so two pictures in one conversation
	// never collide.
	if base := filepath.Base(att.Path); !strings.HasPrefix(base, in.MessageID+"-") {
		t.Errorf("file name = %q, want it prefixed with message id %q", base, in.MessageID)
	}
	if got := readFile(t, att.Path); !bytes.Equal(got, plain) {
		t.Errorf("saved %q, want %q", got, plain)
	}
	if att.Size != int64(len(plain)) {
		t.Errorf("Size = %d, want the decrypted %d", att.Size, len(plain))
	}
	if att.MIMEType != "image/jpeg" {
		t.Errorf("MIMEType = %q, want image/jpeg", att.MIMEType)
	}
	if info, err := os.Stat(att.Path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v (err %v), want 0600: the file may hold private data", info.Mode().Perm(), err)
	}
}

func TestInboundFileDownloaded(t *testing.T) {
	f := newMediaFixture(t, "", 0)
	plain := bytes.Repeat([]byte("report line\n"), 64)

	f.s.PushFile(peer, "report.txt", plain)
	in := start(t, f.a).recv(t)

	att := only(t, in)
	if att.Kind != "file" {
		t.Fatalf("kind = %q, want file", att.Kind)
	}
	if want := in.MessageID + "-report.txt"; filepath.Base(att.Path) != want {
		t.Errorf("file name = %q, want %q", filepath.Base(att.Path), want)
	}
	if got := readFile(t, att.Path); !bytes.Equal(got, plain) {
		t.Errorf("saved %d bytes, want %d", len(got), len(plain))
	}
	if att.MIMEType == "" {
		t.Error("MIMEType is empty, want it derived from the extension")
	}
}

func TestInboundVoiceTranscript(t *testing.T) {
	cdn := newStubCDN(t)
	f := newMediaFixture(t, cdn.URL, 0)
	plain := []byte("silk encoded audio")
	key := testKey()

	f.s.PushMessage(ilinkwire.Message{
		FromUserID: peer,
		ItemList: []ilinkwire.MessageItem{{
			Type: ilinkwire.ItemVoice,
			VoiceItem: &ilinkwire.VoiceItem{
				Media:      cdn.put("voice-1", plain, key, base64.StdEncoding.EncodeToString(key)),
				Text:       "open the log please",
				PlayTime:   3,
				EncodeType: 4,
			},
		}},
	})
	in := start(t, f.a).recv(t)

	// WeChat transcribes the recording itself: the words join the body.
	if in.Text != "open the log please" {
		t.Fatalf("Text = %q, want the transcription", in.Text)
	}
	att := only(t, in)
	if att.Kind != "voice" {
		t.Fatalf("kind = %q, want voice", att.Kind)
	}
	// The recording is kept in the encoding it arrived in.
	if !strings.HasSuffix(att.Path, "-voice.silk") {
		t.Errorf("Path = %q, want it named after the original encoding", att.Path)
	}
	if got := readFile(t, att.Path); !bytes.Equal(got, plain) {
		t.Errorf("saved %q, want %q", got, plain)
	}
}

func TestMediaTooLarge(t *testing.T) {
	cdn := newStubCDN(t)
	f := newMediaFixture(t, cdn.URL, 1) // one megabyte
	limit := int64(1) << 20
	key := testKey()
	run := start(t, f.a)

	// Declared over the limit: refused before a byte crosses the wire.
	big := bytes.Repeat([]byte("x"), int(limit)+4096)
	f.s.PushMessage(ilinkwire.Message{
		FromUserID: peer,
		ItemList: []ilinkwire.MessageItem{{
			Type: ilinkwire.ItemFile,
			FileItem: &ilinkwire.FileItem{
				Media:    cdn.put("big-1", big, key, base64.StdEncoding.EncodeToString(key)),
				FileName: "huge.bin",
				Len:      "1052672",
			},
		}},
	})

	att := only(t, run.recv(t))
	if att.Path != "" {
		t.Errorf("Path = %q, want nothing downloaded", att.Path)
	}
	if att.Size <= limit {
		t.Errorf("Size = %d, want it past the %d byte limit so the core can say why", att.Size, limit)
	}
	if hits := cdn.hits(); hits != 0 {
		t.Errorf("cdn was hit %d times, want the download skipped entirely", hits)
	}

	// Undeclared but over the limit all the same: the read stops early and the
	// size still reports past the ceiling.
	f.s.PushMessage(ilinkwire.Message{
		FromUserID: peer,
		ItemList: []ilinkwire.MessageItem{{
			Type: ilinkwire.ItemFile,
			FileItem: &ilinkwire.FileItem{
				Media:    cdn.put("big-2", big, key, base64.StdEncoding.EncodeToString(key)),
				FileName: "unsized.bin",
			},
		}},
	})

	att = only(t, run.recv(t))
	if att.Path != "" {
		t.Errorf("Path = %q, want the oversized body discarded", att.Path)
	}
	if att.Size <= limit {
		t.Errorf("Size = %d, want it past the %d byte limit", att.Size, limit)
	}
	if entries, err := os.ReadDir(f.inboxDir()); err == nil && len(entries) != 0 {
		t.Errorf("inbox holds %d files, want none", len(entries))
	}
}

func TestUnsafeFileName(t *testing.T) {
	f := newMediaFixture(t, "", 0)
	run := start(t, f.a)

	cases := []struct {
		name string
		sent string
		want string
	}{
		{"parent directory", "../../../etc/passwd", "......etcpasswd"},
		{"windows separators", `..\..\system32\evil.dll`, "....system32evil.dll"},
		{"control characters", "re\x00po\nrt\x1b.txt", "report.txt"},
		{"nothing usable", "../..", "file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.s.PushFile(peer, tc.sent, []byte("payload"))
			in := run.recv(t)

			att := only(t, in)
			if att.Path == "" {
				t.Fatalf("Path is empty, want the file saved under a safe name")
			}
			// Separators and control characters are gone, so the name is one
			// path element and cannot leave the inbox.
			if got, want := filepath.Dir(att.Path), f.inboxDir(); got != want {
				t.Fatalf("saved under %s, want %s", got, want)
			}
			if got, want := filepath.Base(att.Path), in.MessageID+"-"+tc.want; got != want {
				t.Errorf("file name = %q, want %q", got, want)
			}
			if got := readFile(t, att.Path); string(got) != "payload" {
				t.Errorf("contents = %q, want the payload", got)
			}
		})
	}
}

func TestAESKeyVariants(t *testing.T) {
	// A key whose base64 needs the two alphabet-specific characters, so that
	// the standard and URL-safe forms really differ.
	key := bytes.Repeat([]byte{0xfb, 0xff, 0xbf}, 6)[:aes.BlockSize]
	keyHex := hex.EncodeToString(key)

	cases := []struct {
		name   string
		aesKey string
	}{
		{"base64 raw key", base64.StdEncoding.EncodeToString(key)},
		{"base64 raw key unpadded", base64.RawStdEncoding.EncodeToString(key)},
		{"base64url raw key", base64.URLEncoding.EncodeToString(key)},
		{"base64url raw key unpadded", base64.RawURLEncoding.EncodeToString(key)},
		{"base64 of the hexadecimal key", base64.StdEncoding.EncodeToString([]byte(keyHex))},
		{"base64url of the hexadecimal key", base64.RawURLEncoding.EncodeToString([]byte(keyHex))},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cdn := newStubCDN(t)
			f := newMediaFixture(t, cdn.URL, 0)
			plain := []byte("decrypted with " + tc.name)

			f.s.PushMessage(ilinkwire.Message{
				FromUserID: peer,
				ItemList: []ilinkwire.MessageItem{{
					Type: ilinkwire.ItemFile,
					FileItem: &ilinkwire.FileItem{
						Media:    cdn.put("obj", plain, key, tc.aesKey),
						FileName: "payload.bin",
					},
				}},
			})

			att := only(t, start(t, f.a).recv(t))
			if att.Path == "" {
				t.Fatalf("case %d: Path is empty, want aes_key %q understood", i, tc.aesKey)
			}
			if got := readFile(t, att.Path); !bytes.Equal(got, plain) {
				t.Errorf("decrypted %q, want %q", got, plain)
			}
		})
	}

	t.Run("unreadable key", func(t *testing.T) {
		if _, err := parseAESKey("not a key at all!!"); err == nil {
			t.Error("parseAESKey accepted a key it cannot decode")
		}
		if _, err := parseAESKey(""); err == nil {
			t.Error("parseAESKey accepted an empty key")
		}
		// Right alphabet, wrong length.
		if _, err := parseAESKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
			t.Error("parseAESKey accepted a key that is not 16 bytes")
		}
	})
}

func TestFullURLPreferred(t *testing.T) {
	base := newStubCDN(t)
	edge := newStubCDN(t)
	f := newMediaFixture(t, base.URL, 0)
	key := testKey()

	// The same parameter names different bytes on the two hosts, so whichever
	// arrives says which host was asked.
	fromBase := base.put("obj-1", []byte("served by the configured base"), key, base64.StdEncoding.EncodeToString(key))
	fromEdge := edge.put("obj-1", []byte("served by full_url"), key, base64.StdEncoding.EncodeToString(key))
	fromBase.FullURL = edge.URL + "/download?encrypted_query_param=" + url.QueryEscape(fromEdge.EncryptQueryParam)

	f.s.PushMessage(ilinkwire.Message{
		FromUserID: peer,
		ItemList: []ilinkwire.MessageItem{{
			Type:      ilinkwire.ItemImage,
			ImageItem: &ilinkwire.ImageItem{Media: fromBase, URL: "https://cdn.example/photo.png"},
		}},
	})

	att := only(t, start(t, f.a).recv(t))
	if att.Path == "" {
		t.Fatal("Path is empty, want the image downloaded from full_url")
	}
	if got := string(readFile(t, att.Path)); got != "served by full_url" {
		t.Errorf("content = %q, want the bytes from full_url", got)
	}
	if base.hits() != 0 {
		t.Errorf("the configured base was hit %d times, want full_url preferred", base.hits())
	}
	// The name follows the image url, which is all an image item carries.
	if !strings.HasSuffix(att.Path, "-image.png") {
		t.Errorf("Path = %q, want the extension taken from the image url", att.Path)
	}
}

func TestSendFileUploadsAndSends(t *testing.T) {
	f := newMediaFixture(t, "", 0)
	token := f.s.PushText(peer, "send me the log")
	plain := []byte("line one\nline two\n")
	path := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	msg := fsp.Outbound{
		Endpoint:  f.a.endpoint(peer),
		Kind:      fsp.OutFile,
		FilePath:  path,
		DedupeKey: "dk-file",
	}
	state, err := f.a.Send(context.Background(), msg, token)
	if err != nil || state != fsp.DeliveryAccepted {
		t.Fatalf("Send = (%s, %v), want (accepted, nil)", state, err)
	}

	sent := f.s.Sent()
	if len(sent) != 1 || len(sent[0].Items) != 1 {
		t.Fatalf("gateway recorded %+v, want one message with one item", sent)
	}
	item := sent[0].Items[0]
	if item.Type != ilinkwire.ItemFile || item.FileItem == nil {
		t.Fatalf("item = %+v, want a file item", item)
	}
	if item.FileItem.FileName != "run.log" || item.FileItem.Len != "18" {
		t.Errorf("file item = %+v, want run.log of 18 bytes", item.FileItem)
	}
	if item.FileItem.Media == nil || item.FileItem.Media.EncryptType != ilinkwire.EncryptAES128ECB {
		t.Fatalf("media = %+v, want an AES-128-ECB reference", item.FileItem.Media)
	}
	if sent[0].ClientID != "dk-file" {
		t.Errorf("client_id = %q, want the dedupe key so a retry is recognised", sent[0].ClientID)
	}

	files := f.s.SentFiles()
	if len(files) != 1 {
		t.Fatalf("SentFiles() has %d entries, want 1", len(files))
	}
	if !bytes.Equal(files[0].Plain, plain) {
		t.Errorf("gateway decrypted %q, want %q", files[0].Plain, plain)
	}
	if files[0].Name != "run.log" || files[0].ToUserID != peer {
		t.Errorf("SentFiles()[0] = %+v, want run.log addressed to the peer", files[0])
	}

	t.Run("over the limit", func(t *testing.T) {
		small := newMediaFixture(t, "", 1)
		tok := small.s.PushText(peer, "and the big one")
		big := filepath.Join(t.TempDir(), "big.bin")
		if err := os.WriteFile(big, bytes.Repeat([]byte("x"), (1<<20)+1), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		state, err := small.a.Send(context.Background(), fsp.Outbound{
			Endpoint: small.a.endpoint(peer), Kind: fsp.OutFile, FilePath: big, DedupeKey: "dk-big",
		}, tok)
		if state != fsp.DeliveryRejected || err == nil {
			t.Fatalf("Send = (%s, %v), want it rejected for being too large", state, err)
		}
		if len(small.s.Sent()) != 0 {
			t.Errorf("an oversized file reached the gateway: %+v", small.s.Sent())
		}
	})
}

func TestSendImageByExtension(t *testing.T) {
	cases := []struct {
		name string
		want ilinkwire.ItemType
	}{
		{"shot.png", ilinkwire.ItemImage},
		{"shot.JPG", ilinkwire.ItemImage},
		{"photo.jpeg", ilinkwire.ItemImage},
		{"anim.gif", ilinkwire.ItemImage},
		{"modern.webp", ilinkwire.ItemImage},
		{"report.pdf", ilinkwire.ItemFile},
		{"archive.png.gz", ilinkwire.ItemFile},
		{"noextension", ilinkwire.ItemFile},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMediaFixture(t, "", 0)
			token := f.s.PushText(peer, "send it")
			plain := []byte("the bytes of " + tc.name)
			path := filepath.Join(t.TempDir(), tc.name)
			if err := os.WriteFile(path, plain, 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			state, err := f.a.Send(context.Background(), fsp.Outbound{
				Endpoint:  f.a.endpoint(peer),
				Kind:      fsp.OutFile,
				FilePath:  path,
				DedupeKey: "dk-" + tc.name,
			}, token)
			if err != nil || state != fsp.DeliveryAccepted {
				t.Fatalf("Send = (%s, %v), want (accepted, nil)", state, err)
			}

			item := f.s.Sent()[0].Items[0]
			if item.Type != tc.want {
				t.Fatalf("case %d: item type = %d, want %d", i, item.Type, tc.want)
			}
			files := f.s.SentFiles()
			if len(files) != 1 || !bytes.Equal(files[0].Plain, plain) {
				t.Fatalf("SentFiles() = %+v, want the plaintext back", files)
			}
			if tc.want == ilinkwire.ItemImage && item.ImageItem.MidSize == 0 {
				t.Error("mid_size is zero, want the encrypted size")
			}
			if tc.want == ilinkwire.ItemFile && item.FileItem.MD5 == "" {
				t.Error("md5 is empty, want the digest of the plaintext")
			}
		})
	}
}

func TestUploadFailureReported(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"getuploadurl refuses", ilinkwire.PathGetUploadURL},
		{"the cdn refuses", "cdn/upload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMediaFixture(t, "", 0)
			token := f.s.PushText(peer, "send it")
			path := filepath.Join(t.TempDir(), "run.log")
			if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			f.s.FailNext(tc.path, fakeilink.FailHTTP500)

			state, err := f.a.Send(context.Background(), fsp.Outbound{
				Endpoint: f.a.endpoint(peer), Kind: fsp.OutFile, FilePath: path, DedupeKey: "dk-1",
			}, token)
			// Nothing was sent, so the outbox is free to fall back to text
			// rather than wonder whether the file landed.
			if state != fsp.DeliveryRejected {
				t.Fatalf("Send = %s, want rejected", state)
			}
			if err == nil {
				t.Fatal("Send returned no error, want the upload failure reported")
			}
			if len(f.s.Sent()) != 0 {
				t.Errorf("a message reached the gateway: %+v", f.s.Sent())
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		f := newMediaFixture(t, "", 0)
		token := f.s.PushText(peer, "send it")
		state, err := f.a.Send(context.Background(), fsp.Outbound{
			Endpoint:  f.a.endpoint(peer),
			Kind:      fsp.OutFile,
			FilePath:  filepath.Join(t.TempDir(), "gone.log"),
			DedupeKey: "dk-1",
		}, token)
		if state != fsp.DeliveryRejected || err == nil {
			t.Fatalf("Send = (%s, %v), want it rejected", state, err)
		}
	})
}

// stubCDN is a CDN scripted by the test: it serves objects put into it and
// counts what was asked for, which is how "was this downloaded at all" is
// answered.
type stubCDN struct {
	*httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
	asked   int
}

func newStubCDN(t *testing.T) *stubCDN {
	t.Helper()
	c := &stubCDN{objects: map[string][]byte{}}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.asked++
		body, ok := c.objects[r.URL.Query().Get("encrypted_query_param")]
		c.mu.Unlock()

		if r.URL.Path != "/download" || !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(c.Server.Close)
	return c
}

// put encrypts plain under key, parks it under param and returns the reference
// a message carries to it. aesKey is the encoding under test.
func (c *stubCDN) put(param string, plain, key []byte, aesKey string) *ilinkwire.CDNMedia {
	ciphertext, err := encryptECB(plain, key)
	if err != nil {
		panic(err)
	}
	c.mu.Lock()
	c.objects[param] = ciphertext
	c.mu.Unlock()
	return &ilinkwire.CDNMedia{
		EncryptQueryParam: param,
		AESKey:            aesKey,
		EncryptType:       ilinkwire.EncryptAES128ECB,
	}
}

func (c *stubCDN) hits() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.asked
}

// testKey is a fixed AES-128 key, so that a failing test shows the same bytes
// every run.
func testKey() []byte {
	key, err := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	if err != nil {
		panic(err)
	}
	return key
}
