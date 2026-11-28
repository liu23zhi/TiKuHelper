package service

import (
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const agreementContactsKey = "agreement_public_support_contacts"

var ErrAgreementContactRequired = errors.New("at least one public support contact required")

type AgreementContact struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type AgreementContacts struct {
	Channels []AgreementContact `json:"channels"`
}

func normalizeAgreementContacts(in AgreementContacts) (AgreementContacts, error) {
	out := AgreementContacts{Channels: make([]AgreementContact, 0, 3)}
	if in.Channels == nil || len(in.Channels) > 3 {
		return out, ErrAgreementInput
	}
	seen := make(map[string]bool, 3)
	for _, channel := range in.Channels {
		if !utf8.ValidString(channel.Value) || strings.ContainsFunc(channel.Value, func(r rune) bool {
			return unicode.IsControl(r) || unicode.In(r, unicode.Cf)
		}) {
			return out, ErrAgreementInput
		}
		channel.Value = strings.TrimSpace(channel.Value)
		if channel.Value == "" || seen[channel.Kind] {
			return out, ErrAgreementInput
		}
		switch channel.Kind {
		case "email":
			address, err := mail.ParseAddress(channel.Value)
			if len(channel.Value) > 254 || err != nil || address.Name != "" || address.Address != channel.Value ||
				strings.ContainsAny(channel.Value, "\"<>?/#&%:;()") {
				return out, ErrAgreementInput
			}
		case "wecom", "wechat_official":
			// A display name or public account ID, not a QR URL, webhook, or HTML.
			if utf8.RuneCountInString(channel.Value) > 100 || strings.ContainsFunc(channel.Value, func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !strings.ContainsRune(" ._@()（）-·", r)
			}) {
				return out, ErrAgreementInput
			}
		default:
			return out, ErrAgreementInput
		}
		seen[channel.Kind] = true
		out.Channels = append(out.Channels, channel)
	}
	return out, nil
}

// PublicContacts reads only the public, validated presentation data; the
// endpoint never projects the arbitrary system-settings collection.
func (s AgreementStore) PublicContacts() (AgreementContacts, error) {
	row, found, err := getSystemSettingByKey(s.DB, agreementContactsKey)
	if err != nil {
		return AgreementContacts{}, err
	}
	if !found {
		return AgreementContacts{Channels: []AgreementContact{}}, nil
	}
	var data AgreementContacts
	if err := json.Unmarshal([]byte(row.Value), &data); err != nil {
		return AgreementContacts{}, err
	}
	return normalizeAgreementContacts(data)
}

func (s AgreementStore) SetPublicContacts(in AgreementContacts) (AgreementContacts, error) {
	data, err := normalizeAgreementContacts(in)
	if err != nil {
		return AgreementContacts{}, err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return AgreementContacts{}, err
	}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		// Serialize contact changes against Publish. Never leave the active
		// approved agreement pointing users to an empty support catalog.
		var head entity.AgreementHead
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&head, 1).Error; err != nil {
			return err
		}
		if len(data.Channels) == 0 && head.ReleaseID != 0 {
			var active entity.AgreementRelease
			if err := tx.Select("version", "body").First(&active, head.ReleaseID).Error; err != nil {
				return err
			}
			if IsApprovedDiagnosticAgreement(active.Version, AgreementContentHash(active.Body)) {
				return ErrAgreementContactRequired
			}
		}
		return saveSystemSettingValue(tx, agreementContactsKey, string(raw), "协议公开支持联系方式")
	})
	return data, err
}
