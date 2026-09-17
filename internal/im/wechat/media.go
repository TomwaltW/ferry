package wechat

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
)

const (
	// defaultMaxMediaMB is the ceiling on both directions when the config
	// leaves max_media_mb unset.
	defaultMaxMediaMB = 20

	// maxSafeNameLen keeps a hostile file name from hitting the filesystem's
	// own limit. Counted in bytes, which is what the limit is about.
	maxSafeNameLen = 120

	// entryHashLen is how much of the endpoint digest names its inbox folder.
	entryHashLen = 16
)

// imageExts are the extensions that go out as an image item; everything else
// is sent as a file attachment.
var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
}

// maxMediaBytes is the configured ceiling in bytes.
func (a *Adapter) maxMediaBytes() int64 {
	mb := a.cfg.MaxMediaMB
	if mb <= 0 {
		mb = defaultMaxMediaMB
	}
	return int64(mb) << 20
}

// attach turns one media item into an attachment, downloading and decrypting
// it on the way. A failure is not fatal: the attachment comes back without a
// Path and the core tells the user why, so the text of the message still gets
// through.
//
// Size is left at the media's own declared size when nothing was downloaded,
// which is how the core tells "too large" from "download failed".
func (a *Adapter) attach(ctx context.Context, ep fsp.EndpointID, messageID string, it ilinkwire.MessageItem) fsp.Attachment {
	kind, media, key, name, declared := mediaOf(it)
	att := fsp.Attachment{Kind: kind, Name: name, Size: declared}
	if att.Name == "" {
		att.Name = it.MsgID
	}
	att.MIMEType = mime.TypeByExtension(strings.ToLower(filepath.Ext(att.Name)))

	limit := a.maxMediaBytes()
	switch {
	case media == nil:
		return att
	case declared > limit:
		// Refused before a single byte crosses the wire.
		a.log.Info("wechat: media over the limit, not downloading",
			"kind", kind, "bytes", declared, "limit", limit)
		return att
	case a.home == "":
		a.log.Warn("wechat: no home directory, media not downloaded", "kind", kind)
		return att
	}

	plain, err := a.cdn.download(ctx, media, key, limit)
	if err != nil {
		if errors.Is(err, errMediaTooLarge) && att.Size <= limit {
			// The body outgrew the limit without ever saying so: report a size
			// past the ceiling rather than the declared one.
			att.Size = limit + 1
		}
		a.log.Warn("wechat: media download failed", "kind", kind, "error", err)
		return att
	}

	path, err := a.saveMedia(ep, messageID, kind, att.Name, plain)
	if err != nil {
		a.log.Warn("wechat: media save failed", "kind", kind, "error", err)
		return att
	}
	att.Path = path
	att.Size = int64(len(plain))
	att.Name = filepath.Base(path)
	return att
}

// mediaOf pulls the CDN reference out of a message item: its kind, the
// reference itself, the key that opens it, the name it should get on disk and
// the size the gateway claims for it.
func mediaOf(it ilinkwire.MessageItem) (kind string, media *ilinkwire.CDNMedia, aesKey, name string, declared int64) {
	switch it.Type {
	case ilinkwire.ItemImage:
		if it.ImageItem == nil {
			return "image", nil, "", "", 0
		}
		media = it.ImageItem.Media
		aesKey = it.ImageItem.AESKey
		if media != nil && media.AESKey != "" {
			aesKey = media.AESKey
		}
		// The gateway reports encrypted sizes here, which overstate the
		// plaintext by at most one block: good enough to refuse early on.
		declared = max(it.ImageItem.HDSize, it.ImageItem.MidSize)
		return "image", media, aesKey, imageName(it.ImageItem.URL), declared

	case ilinkwire.ItemVoice:
		if it.VoiceItem == nil {
			return "voice", nil, "", "", 0
		}
		media = it.VoiceItem.Media
		if media != nil {
			aesKey = media.AESKey
		}
		// The recording is kept in the encoding WeChat sent, SILK, and the
		// transcription travels in the message text instead.
		return "voice", media, aesKey, "voice.silk", 0

	case ilinkwire.ItemFile:
		if it.FileItem == nil {
			return "file", nil, "", "", 0
		}
		media = it.FileItem.Media
		if media != nil {
			aesKey = media.AESKey
		}
		declared, _ = strconv.ParseInt(it.FileItem.Len, 10, 64)
		return "file", media, aesKey, it.FileItem.FileName, declared
	}
	return "", nil, "", "", 0
}

// imageName derives a file name from the image's url field, which is the only
// name an image item carries. Anything unusable falls back to a plain .jpg.
func imageName(rawURL string) string {
	name := rawURL
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if ext := strings.ToLower(filepath.Ext(name)); imageExts[ext] {
		return "image" + ext
	}
	return "image.jpg"
}

// saveMedia writes the plaintext under <home>/inbox/<entry hash>/, named after
// the message it arrived with. The directory is per endpoint so that one
// conversation cannot overwrite another's downloads.
func (a *Adapter) saveMedia(ep fsp.EndpointID, messageID, kind, name string, plain []byte) (string, error) {
	safe := safeFileName(name)
	if safe == "" {
		safe = defaultMediaName(kind)
	}
	if messageID == "" {
		messageID = "msg"
	}

	dir := filepath.Join(a.home, "inbox", entryHash(ep))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("wechat: create inbox: %w", err)
	}
	path := filepath.Join(dir, safeFileName(messageID)+"-"+safe)
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return "", fmt.Errorf("wechat: write media: %w", err)
	}
	return path, nil
}

// safeFileName reduces a name the sender chose to one path element. Separators
// and control characters are dropped rather than replaced, so no arrangement of
// them can walk out of the inbox directory.
func safeFileName(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f: // control characters, NUL included
			return -1
		case r == '/' || r == '\\' || r == filepath.Separator:
			return -1
		case r == ':': // a drive or stream qualifier on Windows
			return -1
		}
		return r
	}, name)

	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) > maxSafeNameLen {
		cleaned = cleaned[:maxSafeNameLen]
	}
	// What is left may still be a name the filesystem reads as a directory.
	if strings.Trim(cleaned, ".") == "" {
		return ""
	}
	return cleaned
}

func defaultMediaName(kind string) string {
	switch kind {
	case "image":
		return "image.jpg"
	case "voice":
		return "voice.silk"
	case "video":
		return "video.mp4"
	default:
		return "file"
	}
}

// entryHash names the inbox folder of one endpoint. It is a digest rather than
// the endpoint itself so that a peer ID never becomes a path.
func entryHash(ep fsp.EndpointID) string {
	sum := sha256.Sum256([]byte(ep.String()))
	return hex.EncodeToString(sum[:])[:entryHashLen]
}

// sendFile uploads one local file and sends it as an image or a file
// attachment. Like Send it never retries; a failed upload comes back rejected
// so that the outbox can fall back to text.
func (a *Adapter) sendFile(ctx context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error) {
	if msg.FilePath == "" {
		return fsp.DeliveryRejected, errors.New("wechat: outbound file has no path")
	}

	info, err := os.Stat(msg.FilePath)
	if err != nil {
		return fsp.DeliveryRejected, fmt.Errorf("wechat: read outbound file: %w", err)
	}
	limit := a.maxMediaBytes()
	if info.Size() > limit {
		return fsp.DeliveryRejected, fmt.Errorf("wechat: %s is %d bytes: %w", msg.FilePath, info.Size(), errMediaTooLarge)
	}
	plain, err := os.ReadFile(msg.FilePath)
	if err != nil {
		return fsp.DeliveryRejected, fmt.Errorf("wechat: read outbound file: %w", err)
	}
	if int64(len(plain)) > limit {
		return fsp.DeliveryRejected, fmt.Errorf("wechat: %s grew past the limit: %w", msg.FilePath, errMediaTooLarge)
	}

	name := filepath.Base(msg.FilePath)
	asImage := imageExts[strings.ToLower(filepath.Ext(name))]
	mediaType := ilinkwire.UploadFile
	if asImage {
		mediaType = ilinkwire.UploadImage
	}

	param, key, ciphertextLen, err := a.upload(ctx, plain, msg.Endpoint.PeerID, mediaType)
	if err != nil {
		return fsp.DeliveryRejected, err
	}

	media := &ilinkwire.CDNMedia{
		EncryptQueryParam: param,
		AESKey:            wireAESKey(key),
		EncryptType:       ilinkwire.EncryptAES128ECB,
	}
	item := ilinkwire.MessageItem{Type: ilinkwire.ItemFile, FileItem: &ilinkwire.FileItem{
		Media:    media,
		FileName: name,
		MD5:      fmt.Sprintf("%x", md5.Sum(plain)),
		Len:      strconv.Itoa(len(plain)),
	}}
	if asImage {
		item = ilinkwire.MessageItem{Type: ilinkwire.ItemImage, ImageItem: &ilinkwire.ImageItem{
			Media:   media,
			MidSize: ciphertextLen,
		}}
	}

	a.log.Debug("wechat: sending media", "kind", item.Type, "bytes", len(plain))
	return a.deliver(ctx, msg, replyCtx, item)
}

// upload encrypts plain under a single use key and puts it on the CDN,
// returning the parameter a message references it by.
func (a *Adapter) upload(ctx context.Context, plain []byte, to string, mediaType ilinkwire.UploadMediaType) (param string, key []byte, ciphertextLen int64, err error) {
	key, err = newAESKey()
	if err != nil {
		return "", nil, 0, err
	}
	filekey, err := newFileKey()
	if err != nil {
		return "", nil, 0, err
	}

	req := ilinkwire.GetUploadURLReq{
		FileKey:     filekey,
		MediaType:   mediaType,
		ToUserID:    to,
		RawSize:     int64(len(plain)),
		RawFileMD5:  fmt.Sprintf("%x", md5.Sum(plain)),
		FileSize:    paddedSize(int64(len(plain))),
		NoNeedThumb: true,
		// getuploadurl takes the key in plain hexadecimal; the message that
		// follows carries the same key base64 encoded.
		AESKey:   hex.EncodeToString(key),
		BaseInfo: a.cli.baseInfo(),
	}
	var resp ilinkwire.GetUploadURLResp
	if err := a.cli.post(ctx, ilinkwire.PathGetUploadURL, req, &resp); err != nil {
		if sessionExpired(err) {
			a.setOffline("session expired")
			return "", nil, 0, fsp.ErrSessionExpired
		}
		return "", nil, 0, fmt.Errorf("wechat: getuploadurl: %w", err)
	}

	target, err := a.cdn.uploadURL(resp, filekey)
	if err != nil {
		return "", nil, 0, err
	}
	ciphertext, err := encryptECB(plain, key)
	if err != nil {
		return "", nil, 0, err
	}
	param, err = a.cdn.upload(ctx, target, ciphertext)
	if err != nil {
		return "", nil, 0, err
	}
	return param, key, int64(len(ciphertext)), nil
}
