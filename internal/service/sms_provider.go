package service

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type SMSProvider interface {
	SendSMS(phone string, content string) error
}

type SMSBaoProvider struct {
	Endpoint string
	Username string
	Secret   string
	Client   *http.Client
}

func (p SMSBaoProvider) SendSMS(phone string, content string) error {
	endpoint := strings.TrimSpace(p.Endpoint)
	if endpoint == "" {
		endpoint = "https://api.smsbao.com/sms"
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	query := url.Values{}
	query.Set("u", p.Username)
	query.Set("p", p.Secret)
	query.Set("m", phone)
	query.Set("c", content)

	resp, err := client.Get(endpoint + "?" + query.Encode())
	if err != nil {
		return fmt.Errorf("短信发送失败")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	code := strings.TrimSpace(string(body))
	if code == "0" {
		return nil
	}

	errorText := map[string]string{
		"30": "短信宝鉴权失败",
		"40": "短信宝账号不存在",
		"41": "短信宝余额不足",
		"43": "短信宝 IP 受限",
		"50": "短信内容含敏感词",
		"51": "手机号格式错误",
	}[code]
	if errorText == "" {
		errorText = "短信发送失败"
	}
	return fmt.Errorf("%s", errorText)
}

func newSMSProviderFromSettings(settings SystemSettingDetail) SMSProvider {
	return SMSBaoProvider{
		Endpoint: "https://api.smsbao.com/sms",
		Username: settings.SMSBaoUsername,
		Secret:   settings.SMSBaoSecret,
	}
}
