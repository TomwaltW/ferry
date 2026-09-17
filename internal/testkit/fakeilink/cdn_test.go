package fakeilink

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"testing"

	"github.com/TomwaltW/ferry/internal/ilinkwire"
)

// uploadKeyHex is the key a client claims at getuploadurl time; the gateway
// only has to remember it.
const uploadKeyHex = "000102030405060708090a0b0c0d0e0f"

func TestFakeUploadAndCDN(t *testing.T) {
	s := newLoggedIn(t, Options{})

	_, resp := post[ilinkwire.GetUploadURLResp](t, s, ilinkwire.PathGetUploadURL, ilinkwire.GetUploadURLReq{
		FileKey:     "fk-1",
		MediaType:   ilinkwire.UploadFile,
		ToUserID:    owner,
		NoNeedThumb: true,
		AESKey:      uploadKeyHex,
	}, authHeaders(s))
	if resp.Failed() {
		t.Fatalf("getuploadurl answered %+v", resp.APIError)
	}
	if resp.UploadParam != "up-fk-1" {
		t.Fatalf("upload_param = %q, want it derived from the filekey", resp.UploadParam)
	}
	if resp.UploadFullURL != "" {
		t.Errorf("upload_full_url = %q, want the client to build the url from the cdn base", resp.UploadFullURL)
	}

	key, err := hex.DecodeString(uploadKeyHex)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	ciphertext := encryptECB([]byte("contents of a small file"), key)

	uploadURL := s.CDNBaseURL() + "/upload?encrypted_query_param=" +
		url.QueryEscape(resp.UploadParam) + "&filekey=fk-1"
	status, raw, param := upload(t, uploadURL, ciphertext)
	if status != http.StatusOK {
		t.Fatalf("upload: status %d, body %q", status, raw)
	}
	if param == "" {
		t.Fatalf("upload response carries no %s header", uploadParamHeader)
	}

	// The same bytes come back, still encrypted: decrypting is the client's job.
	downloadURL := s.CDNBaseURL() + "/download?encrypted_query_param=" + url.QueryEscape(param)
	status, got := do(t, http.MethodGet, downloadURL, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("download: status %d", status)
	}
	if !bytes.Equal(got, ciphertext) {
		t.Errorf("download returned %d bytes, want the %d uploaded", len(got), len(ciphertext))
	}

	// An unknown filekey was never promised an address, and an unknown object
	// was never stored.
	if status, _, _ := upload(t, s.CDNBaseURL()+"/upload?encrypted_query_param=x&filekey=nope", ciphertext); status != http.StatusForbidden {
		t.Errorf("upload with an unknown filekey: status %d, want 403", status)
	}
	if status, _ := do(t, http.MethodGet, s.CDNBaseURL()+"/download?encrypted_query_param=nope", nil, nil); status != http.StatusNotFound {
		t.Errorf("download of an unknown object: status %d, want 404", status)
	}
}

func TestFakePushImage(t *testing.T) {
	s := newLoggedIn(t, Options{})
	plain := []byte("\x89PNG\r\n\x1a\n not really a png")

	token := s.PushImage(owner, plain)
	if token == "" {
		t.Fatal("PushImage returned no context_token")
	}

	resp := poll(t, s, "")
	if len(resp.Msgs) != 1 || len(resp.Msgs[0].ItemList) != 1 {
		t.Fatalf("getupdates returned %+v, want one message with one item", resp.Msgs)
	}
	item := resp.Msgs[0].ItemList[0]
	if item.Type != ilinkwire.ItemImage || item.ImageItem == nil || item.ImageItem.Media == nil {
		t.Fatalf("item = %+v, want an image item with a cdn reference", item)
	}
	media := item.ImageItem.Media
	if media.EncryptType != ilinkwire.EncryptAES128ECB {
		t.Errorf("encrypt_type = %d, want AES-128-ECB", media.EncryptType)
	}

	// The aes_key travels as base64 of the hexadecimal key, the encoding the
	// real gateway uses.
	decoded, err := base64.StdEncoding.DecodeString(media.AESKey)
	if err != nil {
		t.Fatalf("aes_key is not base64: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("aes_key decodes to %d bytes, want 32 hexadecimal digits", len(decoded))
	}

	downloadURL := s.CDNBaseURL() + "/download?encrypted_query_param=" + url.QueryEscape(media.EncryptQueryParam)
	status, ciphertext := do(t, http.MethodGet, downloadURL, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("download: status %d", status)
	}
	got, err := decryptHexKey(ciphertext, string(decoded))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("decrypted %q, want %q", got, plain)
	}

	// A file push carries the name and size the sender gave it.
	s.PushFile(owner, "notes.txt", []byte("hello"))
	resp = poll(t, s, resp.GetUpdatesBuf)
	file := resp.Msgs[0].ItemList[0].FileItem
	if file == nil || file.FileName != "notes.txt" || file.Len != "5" {
		t.Errorf("file item = %+v, want notes.txt of 5 bytes", file)
	}
}

func TestFakeSentFilesDecrypt(t *testing.T) {
	s := newLoggedIn(t, Options{})
	token := s.PushText(owner, "send me the log")
	plain := []byte("line one\nline two\n")

	// Walk the outbound path the adapter walks: ask for an address, upload the
	// ciphertext, then send a message referencing it.
	_, up := post[ilinkwire.GetUploadURLResp](t, s, ilinkwire.PathGetUploadURL, ilinkwire.GetUploadURLReq{
		FileKey: "fk-out", MediaType: ilinkwire.UploadFile, ToUserID: owner, AESKey: uploadKeyHex,
	}, authHeaders(s))

	key, _ := hex.DecodeString(uploadKeyHex)
	uploadURL := s.CDNBaseURL() + "/upload?encrypted_query_param=" +
		url.QueryEscape(up.UploadParam) + "&filekey=fk-out"
	status, raw, param := upload(t, uploadURL, encryptECB(plain, key))
	if status != http.StatusOK {
		t.Fatalf("upload: status %d, body %q", status, raw)
	}

	_, apiErr := post[ilinkwire.APIError](t, s, ilinkwire.PathSendMessage, ilinkwire.SendMessageReq{
		Msg: &ilinkwire.Message{
			ToUserID:     owner,
			ClientID:     "cid-file",
			ContextToken: token,
			MessageType:  ilinkwire.MsgTypeBot,
			MessageState: ilinkwire.StateFinish,
			ItemList: []ilinkwire.MessageItem{{
				Type: ilinkwire.ItemFile,
				FileItem: &ilinkwire.FileItem{
					FileName: "run.log",
					Media: &ilinkwire.CDNMedia{
						EncryptQueryParam: param,
						AESKey:            base64.StdEncoding.EncodeToString([]byte(uploadKeyHex)),
						EncryptType:       ilinkwire.EncryptAES128ECB,
					},
				},
			}},
		},
	}, authHeaders(s))
	if apiErr.Failed() {
		t.Fatalf("sendmessage answered %+v", apiErr)
	}

	files := s.SentFiles()
	if len(files) != 1 {
		t.Fatalf("SentFiles() has %d entries, want 1", len(files))
	}
	if files[0].Name != "run.log" || files[0].Kind != ilinkwire.ItemFile || files[0].ToUserID != owner {
		t.Errorf("SentFiles()[0] = %+v, want run.log addressed to the owner", files[0])
	}
	if !bytes.Equal(files[0].Plain, plain) {
		t.Errorf("SentFiles()[0].Plain = %q, want %q", files[0].Plain, plain)
	}

	// A text reply is not a file, and neither is a media reference the CDN
	// never received.
	send(t, s, token, "cid-text", "here it is")
	if got := s.SentFiles(); len(got) != 1 {
		t.Errorf("SentFiles() has %d entries after a text reply, want 1", len(got))
	}
}

// upload POSTs a body to the fake CDN and returns the download parameter the
// response names it by.
func upload(t *testing.T, url string, body []byte) (int, []byte, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	raw := make([]byte, 0)
	buf := make([]byte, 512)
	if n, _ := resp.Body.Read(buf); n > 0 {
		raw = buf[:n]
	}
	return resp.StatusCode, raw, resp.Header.Get(uploadParamHeader)
}
