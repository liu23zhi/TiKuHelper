package service

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Same deliberately finite ASCII SVG subset as web/src/utils/safePaymentIcon.mjs.
// It rejects rather than strips. Legacy reads never call this write validator.
var ErrPaymentMethodIconUnsafe = errors.New("SVG 图标不安全或不受支持，请重新选择内置图标或使用静态 SVG")
var iconElements = map[string][]string{
	"svg": {"xmlns", "viewBox", "width", "height", "focusable", "aria-hidden", "role", "data-icon"},
	"g":   {}, "path": {"d"}, "circle": {"cx", "cy", "r"}, "ellipse": {"cx", "cy", "rx", "ry"},
	"rect": {"x", "y", "width", "height", "rx", "ry"}, "line": {"x1", "y1", "x2", "y2"}, "polyline": {"points"}, "polygon": {"points"},
}
var iconCommon = []string{"fill", "stroke", "stroke-width", "stroke-linecap", "stroke-linejoin", "stroke-miterlimit", "stroke-dasharray", "stroke-dashoffset", "opacity", "fill-opacity", "stroke-opacity", "fill-rule", "clip-rule", "transform"}

const iconNumber = `[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?`

var iconNumeric = regexp.MustCompile(`^` + iconNumber + `$`)
var iconNumbers = regexp.MustCompile(`^` + iconNumber + `(?:[ ,\t\r\n]+` + iconNumber + `)*$`)
var iconDimension = regexp.MustCompile(`^(` + iconNumber + `)(px|em|%)?$`)
var iconSeparators = regexp.MustCompile(`[ ,\t\r\n]+`)
var iconWhitespace = regexp.MustCompile(`^[ \t\r\n]+`)
var iconClose = regexp.MustCompile(`^</([A-Za-z][A-Za-z0-9-]*)>`)
var iconOpen = regexp.MustCompile(`^<([A-Za-z][A-Za-z0-9-]*)`)
var iconEnd = regexp.MustCompile(`^[ \t\r\n]*(/?)>`)
var iconAttr = regexp.MustCompile(`^[ \t\r\n]+([A-Za-z][A-Za-z0-9-]*)[ \t\r\n]*=[ \t\r\n]*(?:"([^"<>]*)"|'([^'<>]*)')`)
var iconData = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
var iconColor = regexp.MustCompile(`^(?:none|currentColor|transparent|black|white|red|green|blue|gray|grey|#[0-9a-fA-F]{3}|#[0-9a-fA-F]{4}|#[0-9a-fA-F]{6}|#[0-9a-fA-F]{8})$`)
var iconPathGap = regexp.MustCompile(`^[ ,\t\r\n]+`)
var iconPathNumber = regexp.MustCompile(`^` + iconNumber)
var iconPathCommand = regexp.MustCompile(`^[MmLlHhVvCcSsQqTtAaZz]`)
var iconTransformToken = regexp.MustCompile(`(matrix|translate|scale|rotate|skewX|skewY)\(([^()]*)\)[ \t\r\n]*`)

func iconValidPath(value string) bool {
	sizes := map[byte]int{'m': 2, 'l': 2, 'h': 1, 'v': 1, 'c': 6, 's': 4, 'q': 4, 't': 2, 'a': 7, 'z': 0}
	first := true
	rest := value
	skip := func() {
		if gap := iconPathGap.FindString(rest); gap != "" {
			rest = rest[len(gap):]
		}
	}
	for len(rest) > 0 {
		skip()
		if len(rest) == 0 {
			break
		}
		command := rest[0]
		if command >= 'A' && command <= 'Z' {
			command += 32
		}
		size, ok := sizes[command]
		if !ok || (first && command != 'm') {
			return false
		}
		first = false
		rest = rest[1:]
		count := 0
		for {
			skip()
			if len(rest) == 0 || iconPathCommand.MatchString(rest) {
				break
			}
			if size == 0 {
				return false
			}
			offset := count % size
			token := ""
			if command == 'a' && (offset == 3 || offset == 4) {
				if rest[0] != '0' && rest[0] != '1' {
					return false
				}
				token = rest[:1]
			} else {
				token = iconPathNumber.FindString(rest)
				if token == "" {
					return false
				}
			}
			if !iconBounded(token) {
				return false
			}
			n, _ := strconv.ParseFloat(token, 64)
			if command == 'a' && offset < 2 && n < 0 {
				return false
			}
			count++
			rest = rest[len(token):]
		}
		if size > 0 && (count == 0 || count%size != 0) {
			return false
		}
	}
	return !first
}
func iconValidTransform(value string) bool {
	arities := map[string][]string{"matrix": {"6"}, "translate": {"1", "2"}, "scale": {"1", "2"}, "rotate": {"1", "3"}, "skewX": {"1"}, "skewY": {"1"}}
	matches := iconTransformToken.FindAllStringSubmatchIndex(value, -1)
	previous := 0
	for _, m := range matches {
		name, args := value[m[2]:m[3]], value[m[4]:m[5]]
		if m[0] != previous || !iconList(args, 0) || !iconHas(arities[name], strconv.Itoa(len(iconSeparators.Split(args, -1)))) {
			return false
		}
		previous = m[1]
	}
	return len(matches) > 0 && previous == len(value)
}
func iconHas(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func iconBounded(value string) bool {
	n, e := strconv.ParseFloat(value, 64)
	return iconNumeric.MatchString(value) && e == nil && !math.IsInf(n, 0) && !math.IsNaN(n) && math.Abs(n) <= 1000000
}
func iconList(value string, count int) bool {
	if !iconNumbers.MatchString(value) {
		return false
	}
	a := iconSeparators.Split(value, -1)
	if count > 0 && len(a) != count {
		return false
	}
	for _, v := range a {
		if !iconBounded(v) {
			return false
		}
	}
	return true
}
func iconValidAttr(tag, key, value string) bool {
	if !iconHas(iconElements[tag], key) && !iconHas(iconCommon, key) {
		return false
	}
	switch key {
	case "xmlns":
		return tag == "svg" && value == "http://www.w3.org/2000/svg"
	case "viewBox":
		return iconList(value, 4)
	case "width", "height":
		m := iconDimension.FindStringSubmatch(value)
		if m == nil || !iconBounded(m[1]) {
			return false
		}
		n, _ := strconv.ParseFloat(m[1], 64)
		return n >= 0
	case "aria-hidden", "focusable":
		return value == "true" || value == "false"
	case "role":
		return value == "img"
	case "data-icon":
		return iconData.MatchString(value)
	case "fill", "stroke":
		return iconColor.MatchString(value)
	case "d":
		return iconValidPath(value)
	case "points", "stroke-dasharray":
		return iconList(value, 0)
	case "fill-rule", "clip-rule":
		return value == "nonzero" || value == "evenodd"
	case "stroke-linecap":
		return iconHas([]string{"butt", "round", "square"}, value)
	case "stroke-linejoin":
		return iconHas([]string{"miter", "round", "bevel"}, value)
	case "transform":
		return iconValidTransform(value)
	}
	if strings.Contains(key, "opacity") {
		n, _ := strconv.ParseFloat(value, 64)
		return iconBounded(value) && n >= 0 && n <= 1
	}
	return iconBounded(value)
}

func ValidatePaymentMethodIcon(raw string) error {
	if len(raw) > 16384 {
		return ErrPaymentMethodIconUnsafe
	}
	for _, c := range raw {
		if !(c == 9 || c == 10 || c == 13 || c >= 32 && c <= 126) || strings.ContainsRune("&!?", c) {
			return ErrPaymentMethodIconUnsafe
		}
	}
	source := strings.TrimSpace(raw)
	if source == "" {
		return nil
	}
	stack := []string{}
	root := false
	nodes := 0
	for len(source) > 0 {
		if w := iconWhitespace.FindString(source); w != "" {
			source = source[len(w):]
			continue
		}
		if m := iconClose.FindStringSubmatch(source); m != nil {
			if len(stack) == 0 || stack[len(stack)-1] != m[1] {
				return ErrPaymentMethodIconUnsafe
			}
			stack = stack[:len(stack)-1]
			source = source[len(m[0]):]
			continue
		}
		m := iconOpen.FindStringSubmatch(source)
		if m == nil {
			return ErrPaymentMethodIconUnsafe
		}
		tag := m[1]
		if _, ok := iconElements[tag]; !ok {
			return ErrPaymentMethodIconUnsafe
		}
		if !root && tag != "svg" || root && (len(stack) == 0 || tag == "svg") || len(stack) > 0 && !iconHas([]string{"svg", "g"}, stack[len(stack)-1]) {
			return ErrPaymentMethodIconUnsafe
		}
		source = source[len(m[0]):]
		attrs := map[string]bool{}
		for {
			if end := iconEnd.FindStringSubmatch(source); end != nil {
				nodes++
				if nodes > 256 || len(stack)+1 > 16 {
					return ErrPaymentMethodIconUnsafe
				}
				root = true
				if end[1] == "" {
					stack = append(stack, tag)
				}
				source = source[len(end[0]):]
				break
			}
			a := iconAttr.FindStringSubmatch(source)
			if a == nil {
				return ErrPaymentMethodIconUnsafe
			}
			key, value := a[1], a[2]
			if value == "" {
				value = a[3]
			}
			if attrs[key] || len(attrs) >= 24 || len(value) > 8192 || !iconValidAttr(tag, key, value) {
				return ErrPaymentMethodIconUnsafe
			}
			attrs[key] = true
			source = source[len(a[0]):]
		}
	}
	if !root || len(stack) != 0 {
		return ErrPaymentMethodIconUnsafe
	}
	return nil
}
