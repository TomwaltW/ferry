package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkauth "github.com/larksuite/oapi-sdk-go/v3/service/auth/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/TomwaltW/ferry/internal/config"
)

// tooManyRequests is the Feishu error code for a caller going too fast. It is
// the one code this package turns into ErrRateLimited, because it is the only
// one retrying helps with.
const tooManyRequests = 99991400

// receiveIDTypeOpenID names the kind of ID the send calls address.
const receiveIDTypeOpenID = "open_id"

// The message types this package sends.
const (
	msgTypeText = larkim.MsgTypeText
	msgTypeFile = larkim.MsgTypeFile
	msgTypeCard = larkim.MsgTypeInteractive
)

// toastInfo is the toast type used to acknowledge a click.
const toastInfo = "info"

// fileTypeStream is what a file of no particular kind is uploaded as.
const fileTypeStream = "stream"

// sdkClient is the thin wrapper around the official SDK: the long-lived
// connection comes from the ws package, the sends from the ordinary API
// client. It holds no logic of its own, which is why the tests drive Adapter
// through the Client interface instead.
type sdkClient struct {
	appID     string
	appSecret string
	domain    string
	api       *lark.Client
}

func newSDKClient(opts Options) (*sdkClient, error) {
	cfg := opts.Config.WithDefaults()
	if cfg.AppID == "" {
		return nil, errors.New("feishu: app_id is required")
	}
	if opts.Secret == "" {
		return nil, errors.New("feishu: the app secret is missing; run ferry login feishu")
	}
	domain := domainURL(cfg.Domain)
	return &sdkClient{
		appID:     cfg.AppID,
		appSecret: opts.Secret,
		domain:    domain,
		api:       lark.NewClient(cfg.AppID, opts.Secret, lark.WithOpenBaseUrl(domain)),
	}, nil
}

// domainURL maps the configured domain onto the SDK's base URL.
func domainURL(domain string) string {
	if domain == config.FeishuDomainLark {
		return lark.LarkBaseUrl
	}
	return lark.FeishuBaseUrl
}

// Start opens the WebSocket connection and blocks until ctx ends or the SDK
// gives up reconnecting.
func (c *sdkClient) Start(ctx context.Context, onMessage func(Message), onCardAction func(CardAction) (string, bool)) error {
	handler := larkdispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(_ context.Context, ev *larkim.P2MessageReceiveV1) error {
			if m, ok := convertMessage(ev); ok {
				onMessage(m)
			}
			return nil
		}).
		// Card clicks arrive over the same connection (card.action.trigger),
		// so no public callback address is needed.
		OnP2CardActionTrigger(func(_ context.Context, ev *larkcallback.CardActionTriggerEvent) (*larkcallback.CardActionTriggerResponse, error) {
			// An empty response acknowledges the callback without a toast; a
			// nil one would reach Feishu as the JSON null.
			resp := &larkcallback.CardActionTriggerResponse{}
			act, ok := convertCardAction(ev)
			if !ok {
				return resp, nil
			}
			if toast, ok := onCardAction(act); ok && toast != "" {
				resp.Toast = &larkcallback.Toast{Type: toastInfo, Content: toast}
			}
			return resp, nil
		})
	cli := larkws.NewClient(c.appID, c.appSecret,
		larkws.WithEventHandler(handler),
		larkws.WithDomain(c.domain),
		larkws.WithAutoReconnect(true),
	)
	if err := cli.Start(ctx); err != nil {
		return fmt.Errorf("feishu: long-lived connection: %w", err)
	}
	return nil
}

// convertMessage narrows one SDK event to what this package needs. Anything
// that is not a text message in a direct chat is dropped here.
func convertMessage(ev *larkim.P2MessageReceiveV1) (Message, bool) {
	if ev == nil || ev.Event == nil || ev.Event.Message == nil {
		return Message{}, false
	}
	msg := ev.Event.Message
	m := Message{
		MessageID:  deref(msg.MessageId),
		ChatType:   deref(msg.ChatType),
		Text:       textOf(deref(msg.MessageType), deref(msg.Content)),
		CreateTime: millisTime(deref(msg.CreateTime)),
	}
	if ev.Event.Sender != nil && ev.Event.Sender.SenderId != nil {
		m.OpenID = deref(ev.Event.Sender.SenderId.OpenId)
	}
	return m, true
}

// convertCardAction narrows one card callback to what this package needs.
func convertCardAction(ev *larkcallback.CardActionTriggerEvent) (CardAction, bool) {
	if ev == nil || ev.Event == nil || ev.Event.Action == nil {
		return CardAction{}, false
	}
	act := CardAction{Token: ev.Event.Token, Value: ev.Event.Action.Value}
	if ev.EventV2Base != nil && ev.EventV2Base.Header != nil {
		act.EventID = ev.EventV2Base.Header.EventID
	}
	if ev.Event.Operator != nil {
		act.OpenID = ev.Event.Operator.OpenID
	}
	return act, true
}

// textOf pulls the text out of a message body, which Feishu delivers as a JSON
// string whose shape depends on the message type.
func textOf(msgType, content string) string {
	if msgType != msgTypeText || content == "" {
		return ""
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &body); err != nil {
		return ""
	}
	return body.Text
}

func millisTime(s string) time.Time {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (c *sdkClient) SendText(ctx context.Context, openID, text string) (string, error) {
	content, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if err != nil {
		return "", fmt.Errorf("feishu: encode a text message: %w", err)
	}
	return c.create(ctx, openID, msgTypeText, string(content))
}

// SendCard sends an approval card as an interactive message.
func (c *sdkClient) SendCard(ctx context.Context, openID string, card Card) (string, error) {
	content, err := cardJSON(card)
	if err != nil {
		return "", err
	}
	return c.create(ctx, openID, msgTypeCard, content)
}

// SendFile uploads the file and then sends the key it got back.
func (c *sdkClient) SendFile(ctx context.Context, openID, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("feishu: open %s: %w", path, err)
	}
	defer f.Close()

	upload, err := c.api.Im.File.Create(ctx, larkim.NewCreateFileReqBuilder().
		Body(larkim.NewCreateFileReqBodyBuilder().
			FileType(fileTypeStream).
			FileName(filepath.Base(path)).
			File(f).
			Build()).
		Build())
	if err != nil {
		return "", fmt.Errorf("feishu: upload %s: %w", path, err)
	}
	if !upload.Success() {
		return "", apiError("upload "+filepath.Base(path), upload.Code, upload.Msg)
	}
	if upload.Data == nil || upload.Data.FileKey == nil {
		return "", errors.New("feishu: the upload returned no file key")
	}

	content, err := json.Marshal(struct {
		FileKey string `json:"file_key"`
	}{FileKey: *upload.Data.FileKey})
	if err != nil {
		return "", fmt.Errorf("feishu: encode a file message: %w", err)
	}
	return c.create(ctx, openID, msgTypeFile, string(content))
}

func (c *sdkClient) create(ctx context.Context, openID, msgType, content string) (string, error) {
	resp, err := c.api.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDTypeOpenID).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(openID).
			MsgType(msgType).
			Content(content).
			Build()).
		Build())
	if err != nil {
		return "", fmt.Errorf("feishu: send a %s message: %w", msgType, err)
	}
	if !resp.Success() {
		return "", apiError("send a "+msgType+" message", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", nil
	}
	return *resp.Data.MessageId, nil
}

// Ping exchanges the app ID and secret for a tenant token, which is the
// cheapest proof that the credentials are good and needs no extra scope.
func (c *sdkClient) Ping(ctx context.Context) error {
	resp, err := c.api.Auth.TenantAccessToken.Internal(ctx, larkauth.NewInternalTenantAccessTokenReqBuilder().
		Body(larkauth.NewInternalTenantAccessTokenReqBodyBuilder().
			AppId(c.appID).
			AppSecret(c.appSecret).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("feishu: check the credentials: %w", err)
	}
	if !resp.Success() {
		return apiError("check the credentials", resp.Code, resp.Msg)
	}
	return nil
}

// apiError keeps the Feishu error code out of the rest of the package: a
// caller only has to know whether retrying makes sense.
func apiError(what string, code int, msg string) error {
	if code == tooManyRequests {
		return fmt.Errorf("feishu: %s: %w", what, ErrRateLimited)
	}
	return fmt.Errorf("feishu: %s: %s (code %d)", what, msg, code)
}

var _ Client = (*sdkClient)(nil)
