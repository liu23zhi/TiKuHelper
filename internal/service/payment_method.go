package service

import (
	"bytes"
	"errors"
	"regexp"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"gorm.io/gorm"
)

var ErrPaymentMethodNameRequired = errors.New("支付方式名称不能为空")
var ErrPaymentMethodEPayTypeRequired = errors.New("易支付通道值不能为空")
var ErrPaymentMethodIconUnsafe = errors.New("支付方式图标 SVG 包含不安全内容")
var ErrPaymentMethodNotFound = errors.New("支付方式不存在")

const maxPaymentMethodIconSVGBytes = 64 * 1024

var paymentMethodSVGElements = map[string]struct{}{
	"svg": {}, "g": {}, "path": {}, "circle": {}, "ellipse": {}, "rect": {},
	"line": {}, "polyline": {}, "polygon": {}, "defs": {}, "lineargradient": {},
	"radialgradient": {}, "stop": {}, "clippath": {}, "mask": {}, "title": {},
	"desc": {}, "text": {}, "tspan": {},
}

var paymentMethodSVGAttributes = map[string]struct{}{
	"aria-hidden": {}, "class": {}, "clip-path": {}, "clip-rule": {}, "color": {},
	"color-interpolation": {}, "color-interpolation-filters": {}, "cx": {}, "cy": {},
	"d": {}, "fill": {}, "fill-opacity": {}, "fill-rule": {}, "focusable": {},
	"gradient-transform": {}, "gradientunits": {}, "height": {}, "id": {}, "mask": {},
	"mask-type": {}, "offset": {}, "opacity": {}, "points": {}, "preserveaspectratio": {},
	"r": {}, "rect": {}, "role": {}, "rx": {}, "ry": {}, "spreadmethod": {},
	"stop-color": {}, "stop-opacity": {}, "stroke": {}, "stroke-linecap": {},
	"stroke-linejoin": {}, "stroke-miterlimit": {}, "stroke-opacity": {}, "stroke-width": {},
	"transform": {}, "version": {}, "viewbox": {}, "viewtarget": {}, "width": {},
	"x": {}, "x1": {}, "x2": {}, "xmlns": {}, "xmlns:xlink": {}, "y": {}, "y1": {}, "y2": {},
}

var paymentMethodSVGInternalURL = regexp.MustCompile(`^url\(\s*#[a-zA-Z0-9_.:-]+\s*\)$`)

type CreatePaymentMethodInput struct {
	Name     string `json:"name"`
	IconSVG  string `json:"iconSvg"`
	EPayType string `json:"epayType"`
	Enabled  bool   `json:"enabled"`
	Sort     int    `json:"sort"`
	Remark   string `json:"remark"`
}

type UpdatePaymentMethodInput struct {
	Name     string `json:"name"`
	IconSVG  string `json:"iconSvg"`
	EPayType string `json:"epayType"`
	Enabled  bool   `json:"enabled"`
	Sort     int    `json:"sort"`
	Remark   string `json:"remark"`
}

func ListPaymentMethodsWithDB(db *gorm.DB, includeDisabled bool) ([]entity.PaymentMethod, error) {
	query := db.Model(&entity.PaymentMethod{}).Order("sort asc, id asc")
	if !includeDisabled {
		query = query.Where("enabled = ?", true)
	}

	var methods []entity.PaymentMethod
	if err := query.Find(&methods).Error; err != nil {
		return nil, err
	}
	for index := range methods {
		safeIcon, sanitizeErr := sanitizePaymentMethodIconSVG(methods[index].IconSVG)
		if sanitizeErr != nil {
			// Older records predate validation. Never return an unsafe icon to a
			// client even if it was inserted before this validation existed.
			methods[index].IconSVG = ""
			continue
		}
		methods[index].IconSVG = safeIcon
	}
	return methods, nil
}

func CreatePaymentMethodWithDB(db *gorm.DB, input CreatePaymentMethodInput) (entity.PaymentMethod, error) {
	method, err := normalizePaymentMethodInput(input.Name, input.IconSVG, input.EPayType, input.Enabled, input.Sort, input.Remark)
	if err != nil {
		return entity.PaymentMethod{}, err
	}
	if err := db.Create(&method).Error; err != nil {
		return entity.PaymentMethod{}, err
	}
	return method, nil
}

func UpdatePaymentMethodWithDB(db *gorm.DB, paymentMethodID int32, input UpdatePaymentMethodInput) (entity.PaymentMethod, error) {
	method, err := loadPaymentMethodByID(db, paymentMethodID)
	if err != nil {
		return entity.PaymentMethod{}, err
	}

	normalized, err := normalizePaymentMethodInput(input.Name, input.IconSVG, input.EPayType, input.Enabled, input.Sort, input.Remark)
	if err != nil {
		return entity.PaymentMethod{}, err
	}

	method.Name = normalized.Name
	method.IconSVG = normalized.IconSVG
	method.EPayType = normalized.EPayType
	method.Enabled = normalized.Enabled
	method.Sort = normalized.Sort
	method.Remark = normalized.Remark

	if err := db.Save(&method).Error; err != nil {
		return entity.PaymentMethod{}, err
	}
	return method, nil
}

func DeletePaymentMethodWithDB(db *gorm.DB, paymentMethodID int32) error {
	method, err := loadPaymentMethodByID(db, paymentMethodID)
	if err != nil {
		return err
	}
	return db.Delete(&method).Error
}

func normalizePaymentMethodInput(name string, iconSVG string, epayType string, enabled bool, sort int, remark string) (entity.PaymentMethod, error) {
	normalizedName := strings.TrimSpace(name)
	if normalizedName == "" {
		return entity.PaymentMethod{}, ErrPaymentMethodNameRequired
	}

	normalizedEPayType := strings.TrimSpace(epayType)
	if normalizedEPayType == "" {
		return entity.PaymentMethod{}, ErrPaymentMethodEPayTypeRequired
	}

	normalizedIcon, err := sanitizePaymentMethodIconSVG(iconSVG)
	if err != nil {
		return entity.PaymentMethod{}, err
	}

	return entity.PaymentMethod{
		Name:     normalizedName,
		IconSVG:  normalizedIcon,
		EPayType: normalizedEPayType,
		Enabled:  enabled,
		Sort:     sort,
		Remark:   strings.TrimSpace(remark),
	}, nil
}

// sanitizePaymentMethodIconSVG parses and serializes a deliberately small SVG
// subset. User-provided SVG is rendered with v-html in the browser, so unknown
// elements, event handlers, styles, external references, and non-SVG roots are
// rejected before the value reaches storage or an API response.
func sanitizePaymentMethodIconSVG(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len([]byte(trimmed)) > maxPaymentMethodIconSVGBytes {
		return "", ErrPaymentMethodIconUnsafe
	}

	nodes, err := html.ParseFragment(strings.NewReader(trimmed), &html.Node{Type: html.ElementNode, Data: "svg", DataAtom: atom.Svg, Namespace: "svg"})
	if err != nil || len(nodes) != 1 || nodes[0].Type != html.ElementNode || strings.ToLower(nodes[0].Data) != "svg" {
		return "", ErrPaymentMethodIconUnsafe
	}
	if err := validatePaymentMethodSVGNode(nodes[0], true); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := html.Render(&buf, nodes[0]); err != nil {
		return "", ErrPaymentMethodIconUnsafe
	}
	return strings.TrimSpace(buf.String()), nil
}

func validatePaymentMethodSVGNode(node *html.Node, root bool) error {
	if node.Type == html.TextNode {
		return nil
	}
	if node.Type != html.ElementNode {
		return ErrPaymentMethodIconUnsafe
	}
	name := strings.ToLower(strings.TrimSpace(node.Data))
	if _, ok := paymentMethodSVGElements[name]; !ok || (root && name != "svg") {
		return ErrPaymentMethodIconUnsafe
	}
	for _, attr := range node.Attr {
		key := strings.ToLower(strings.TrimSpace(attr.Key))
		if strings.HasPrefix(key, "on") || strings.EqualFold(key, "style") {
			return ErrPaymentMethodIconUnsafe
		}
		if _, ok := paymentMethodSVGAttributes[key]; !ok {
			return ErrPaymentMethodIconUnsafe
		}
		value := strings.TrimSpace(attr.Val)
		if key == "xmlns" && value != "http://www.w3.org/2000/svg" {
			return ErrPaymentMethodIconUnsafe
		}
		if key == "xmlns:xlink" && value != "http://www.w3.org/1999/xlink" {
			return ErrPaymentMethodIconUnsafe
		}
		if !isSafePaymentMethodSVGAttributeValue(key, value) {
			return ErrPaymentMethodIconUnsafe
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if err := validatePaymentMethodSVGNode(child, false); err != nil {
			return err
		}
	}
	return nil
}

func isSafePaymentMethodSVGAttributeValue(key, value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if key == "xmlns" || key == "xmlns:xlink" {
		return true
	}
	if strings.ContainsAny(value, "<>\"'") || strings.Contains(lower, "javascript:") || strings.Contains(lower, "vbscript:") || strings.Contains(lower, "data:") || strings.Contains(lower, "http:") || strings.Contains(lower, "https:") {
		return false
	}
	if strings.Contains(lower, "url(") {
		return paymentMethodSVGInternalURL.MatchString(lower)
	}
	if key == "href" || key == "xlink:href" {
		return false
	}
	return true
}

func loadPaymentMethodByID(db *gorm.DB, paymentMethodID int32) (entity.PaymentMethod, error) {
	var method entity.PaymentMethod
	if err := db.Where("id = ?", paymentMethodID).First(&method).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.PaymentMethod{}, ErrPaymentMethodNotFound
		}
		return entity.PaymentMethod{}, err
	}
	return method, nil
}
