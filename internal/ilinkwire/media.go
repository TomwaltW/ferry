package ilinkwire

// Media upload types, added in P2. Field names follow openilink-sdk-go v0.6.0
// types.go. Encryption is AES-128-ECB with PKCS#7 padding; see cdn.go there.

type UploadMediaType int

const (
	UploadImage UploadMediaType = 1
	UploadVideo UploadMediaType = 2
	UploadFile  UploadMediaType = 3
	UploadVoice UploadMediaType = 4
)

type GetUploadURLReq struct {
	FileKey       string          `json:"filekey,omitempty"`
	MediaType     UploadMediaType `json:"media_type,omitempty"`
	ToUserID      string          `json:"to_user_id,omitempty"`
	RawSize       int64           `json:"rawsize,omitempty"`
	RawFileMD5    string          `json:"rawfilemd5,omitempty"`
	FileSize      int64           `json:"filesize,omitempty"` // size of the ciphertext
	ThumbRawSize  int64           `json:"thumb_rawsize,omitempty"`
	ThumbRawMD5   string          `json:"thumb_rawfilemd5,omitempty"`
	ThumbFileSize int64           `json:"thumb_filesize,omitempty"`
	NoNeedThumb   bool            `json:"no_need_thumb,omitempty"`
	AESKey        string          `json:"aeskey,omitempty"`
	BaseInfo      *BaseInfo       `json:"base_info,omitempty"`
}

type GetUploadURLResp struct {
	APIError
	UploadParam      string `json:"upload_param,omitempty"`
	ThumbUploadParam string `json:"thumb_upload_param,omitempty"`
	UploadFullURL    string `json:"upload_full_url,omitempty"`
}
