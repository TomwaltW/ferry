// Package ilinkwire holds the on-the-wire constants and JSON shapes of the
// iLink bot API. Field names and values follow openilink-sdk-go v0.6.0
// (types.go, client.go, errors.go), verified on 2026-09-17. The package only
// describes the protocol; it performs no I/O.
package ilinkwire

const (
	DefaultBaseURL        = "https://ilinkai.weixin.qq.com"
	DefaultChannelVersion = "2.1.6" // written into base_info.channel_version of every request; configurable

	PathGetBotQRCode    = "ilink/bot/get_bot_qrcode"    // GET, query parameter bot_type=3
	PathGetQRCodeStatus = "ilink/bot/get_qrcode_status" // GET, query parameter qrcode=<key>
	PathGetUpdates      = "ilink/bot/getupdates"        // POST, long poll, about 35 seconds server side
	PathSendMessage     = "ilink/bot/sendmessage"       // POST, response body is {} on success
	PathGetConfig       = "ilink/bot/getconfig"         // POST
	PathSendTyping      = "ilink/bot/sendtyping"        // POST
	PathGetUploadURL    = "ilink/bot/getuploadurl"      // POST, used from P2 on

	HeaderAuthType      = "AuthorizationType" // constant value AuthTypeValue
	AuthTypeValue       = "ilink_bot_token"
	HeaderUIN           = "X-WECHAT-UIN"  // randomly generated per request, see openilink-sdk-go randomWechatUIN
	HeaderAuthorization = "Authorization" // "Bearer <bot_token>"

	ErrCodeSessionExpired = -14 // the session is expired when either ret or errcode equals it
)

type BaseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
}

type APIError struct {
	Ret     int    `json:"ret,omitempty"`
	ErrCode int    `json:"errcode,omitempty"`
	ErrMsg  string `json:"errmsg,omitempty"`
}

// Failed reports whether the gateway rejected the call.
func (e APIError) Failed() bool { return e.Ret != 0 || e.ErrCode != 0 }

// SessionExpired reports whether the bot session has to be re-established by
// scanning a new QR code.
func (e APIError) SessionExpired() bool {
	return e.Ret == ErrCodeSessionExpired || e.ErrCode == ErrCodeSessionExpired
}

type QRCodeResp struct {
	QRCode           string `json:"qrcode"`             // key used when polling
	QRCodeImgContent string `json:"qrcode_img_content"` // payload the QR code encodes
}

type QRStatusResp struct {
	Status       string `json:"status"` // wait | scaned | confirmed | expired | scaned_but_redirect
	BotToken     string `json:"bot_token,omitempty"`
	ILinkBotID   string `json:"ilink_bot_id,omitempty"`
	BaseURL      string `json:"baseurl,omitempty"`
	ILinkUserID  string `json:"ilink_user_id,omitempty"` // the scanning user, i.e. the owner
	RedirectHost string `json:"redirect_host,omitempty"`
}

type (
	MessageType  int
	MessageState int
	ItemType     int
)

const (
	MsgTypeUser MessageType = 1
	MsgTypeBot  MessageType = 2

	StateNew        MessageState = 0
	StateGenerating MessageState = 1
	StateFinish     MessageState = 2

	ItemText  ItemType = 1
	ItemImage ItemType = 2
	ItemVoice ItemType = 3
	ItemFile  ItemType = 4
	ItemVideo ItemType = 5

	EncryptAES128ECB = 1
)

type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

type TextItem struct {
	Text string `json:"text,omitempty"`
}

type ImageItem struct {
	Media       *CDNMedia `json:"media,omitempty"`
	ThumbMedia  *CDNMedia `json:"thumb_media,omitempty"`
	AESKey      string    `json:"aeskey,omitempty"`
	URL         string    `json:"url,omitempty"`
	MidSize     int64     `json:"mid_size,omitempty"`
	ThumbSize   int64     `json:"thumb_size,omitempty"`
	ThumbHeight int       `json:"thumb_height,omitempty"`
	ThumbWidth  int       `json:"thumb_width,omitempty"`
	HDSize      int64     `json:"hd_size,omitempty"`
}

type VoiceItem struct {
	Media         *CDNMedia `json:"media,omitempty"`
	PlayTime      int       `json:"playtime,omitempty"`
	Text          string    `json:"text,omitempty"` // transcription provided by WeChat
	EncodeType    int       `json:"encode_type,omitempty"`
	BitsPerSample int       `json:"bits_per_sample,omitempty"`
	SampleRate    int       `json:"sample_rate,omitempty"`
}

type FileItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"`
}

type MessageItem struct {
	Type      ItemType   `json:"type,omitempty"`
	MsgID     string     `json:"msg_id,omitempty"`
	TextItem  *TextItem  `json:"text_item,omitempty"`
	ImageItem *ImageItem `json:"image_item,omitempty"`
	VoiceItem *VoiceItem `json:"voice_item,omitempty"`
	FileItem  *FileItem  `json:"file_item,omitempty"`
}

type Message struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	CreateTimeMs int64         `json:"create_time_ms,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	GroupID      string        `json:"group_id,omitempty"` // non-empty means a group message
	MessageType  MessageType   `json:"message_type,omitempty"`
	MessageState MessageState  `json:"message_state,omitempty"`
	ItemList     []MessageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
}

type GetUpdatesReq struct {
	GetUpdatesBuf string    `json:"get_updates_buf"`
	BaseInfo      *BaseInfo `json:"base_info,omitempty"`
}

type GetUpdatesResp struct {
	APIError
	Msgs                 []Message `json:"msgs,omitempty"`
	GetUpdatesBuf        string    `json:"get_updates_buf,omitempty"`
	LongPollingTimeoutMs int64     `json:"longpolling_timeout_ms,omitempty"`
}

type SendMessageReq struct {
	Msg      *Message  `json:"msg,omitempty"` // message_type=2, message_state=2, must carry context_token and client_id
	BaseInfo *BaseInfo `json:"base_info,omitempty"`
}

type GetConfigReq struct {
	ILinkUserID  string    `json:"ilink_user_id,omitempty"`
	ContextToken string    `json:"context_token,omitempty"`
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}

type GetConfigResp struct {
	APIError
	TypingTicket string `json:"typing_ticket,omitempty"`
}

type SendTypingReq struct {
	ILinkUserID  string    `json:"ilink_user_id,omitempty"`
	TypingTicket string    `json:"typing_ticket,omitempty"`
	Status       int       `json:"status,omitempty"` // 1 typing, 2 cancel
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}
