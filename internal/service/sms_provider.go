package service

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/pkg/util"
)

type SMSProvider interface {
	SendSMS(phone string, content string) error
}

type SMSBaoProvider struct {
	Endpoint              string
	DomesticEndpoint      string
	InternationalEndpoint string
	Username              string
	Secret                string
	CredentialType        string
	RegionMode            string
	Client                *http.Client
}

func (p SMSBaoProvider) SendSMS(phone string, content string) error {
	normalizedPhone, err := NormalizeMainlandPhoneInput(phone)
	if err != nil {
		return err
	}
	// The domestic API only accepts eleven ASCII digits, including when a
	// caller supplied the explicitly supported legacy +86 form.
	if !mainlandPhonePattern.MatchString(normalizedPhone) {
		return fmt.Errorf("手机号格式不正确")
	}
	endpoint, err := p.endpointForPhoneRegion(PhoneRegionMainland)
	if err != nil {
		return err
	}
	client := p.Client
	if client == nil {
		client = util.NewSafeHTTPClient(10 * time.Second)
	}
	query := url.Values{}
	query.Set("u", p.Username)
	query.Set("p", p.credentialValue())
	query.Set("m", normalizedPhone)
	query.Set("c", content)

	resp, err := client.Get(endpoint + "?" + query.Encode())
	if err != nil {
		return fmt.Errorf("短信发送失败")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
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

func (p SMSBaoProvider) endpointForPhoneRegion(phoneRegion string) (string, error) {
	regionMode := normalizeSMSBaoRegionMode(p.RegionMode)
	if regionMode == "domestic" && phoneRegion != PhoneRegionMainland {
		return "", fmt.Errorf("当前短信配置仅允许发送国内手机号")
	}
	if regionMode == "international" && phoneRegion != PhoneRegionInternational {
		return "", fmt.Errorf("当前短信配置仅允许发送国际手机号")
	}

	if phoneRegion == PhoneRegionInternational {
		endpoint := strings.TrimSpace(p.InternationalEndpoint)
		if endpoint == "" {
			endpoint = "https://api.smsbao.com/wsms"
		}
		return endpoint, nil
	}

	endpoint := strings.TrimSpace(p.DomesticEndpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(p.Endpoint)
	}
	if endpoint == "" {
		endpoint = "https://api.smsbao.com/sms"
	}
	return endpoint, nil
}

func (p SMSBaoProvider) credentialValue() string {
	secret := strings.TrimSpace(p.Secret)
	if normalizeSMSBaoCredentialType(p.CredentialType) != "password" {
		return secret
	}
	sum := md5.Sum([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func newSMSProviderFromSettings(settings SystemSettingDetail) SMSProvider {
	return SMSBaoProvider{
		DomesticEndpoint:      "https://api.smsbao.com/sms",
		InternationalEndpoint: "https://api.smsbao.com/wsms",
		Username:              settings.SMSBaoUsername,
		Secret:                settings.SMSBaoSecret,
		CredentialType:        settings.SMSBaoCredentialType,
		RegionMode:            settings.SMSBaoRegionMode,
	}
}
