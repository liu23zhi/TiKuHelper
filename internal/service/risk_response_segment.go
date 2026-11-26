package service

import (
	"encoding/json"
	"net/netip"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type blacklistIPv4Range struct{ Begin, End netip.Addr }

func parseBlacklistIPv4Range(raw string) (blacklistIPv4Range, bool) {
	begin, end, split := strings.Cut(raw, "-")
	lo, errLo := netip.ParseAddr(strings.TrimSpace(begin))
	hi, errHi := netip.ParseAddr(strings.TrimSpace(end))
	if !split || errLo != nil || errHi != nil || !lo.Is4() || !hi.Is4() ||
		lo.Compare(hi) > 0 || attributableRiskSourceIP(lo.String()) == "" || attributableRiskSourceIP(hi.String()) == "" {
		return blacklistIPv4Range{}, false
	}
	return blacklistIPv4Range{lo, hi}, true
}

func normalizeAutomaticBlacklistIPv4Range(raw string) (NormalizedBlacklistValue, error) {
	bounds, ok := parseBlacklistIPv4Range(raw)
	if !ok {
		return NormalizedBlacklistValue{}, ErrInvalidBlacklistPattern
	}
	pattern := bounds.String()
	return NormalizedBlacklistValue{Kind: BlacklistKindIP, Pattern: pattern, MatchType: blacklistMatchIPv4Range,
		ValueHash: BlacklistPatternHashHex(pattern), ValueMasked: MaskBlacklistValue(BlacklistKindIP, pattern, blacklistMatchIPv4Range)}, nil
}

func (r blacklistIPv4Range) String() string { return r.Begin.String() + "-" + r.End.String() }
func (r blacklistIPv4Range) contains(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.Is4() && r.Begin.Compare(ip) <= 0 && r.End.Compare(ip) >= 0
}

// Only already-cached standard provider evidence is trusted. An exact IP
// response takes precedence: if it lacks bounds, do not revive an older range.
// On a segment hit require text/numeric consistency, sample containment and
// agreement among all rows at the winning width. No upstream lookup here.
func autoEvidenceSegment(db *gorm.DB, ip string) (string, string) {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is4() {
		return "", "segment_family_unsupported"
	}
	if db.Migrator().HasTable(entity.TableNameLocationCache) {
		var rows []entity.LocationCache
		if err := db.Where("kind = ? AND lookup_key = ? AND source IN ?", entity.LocationKindIP, locationCacheLookupKey(ip), []string{"", "standard"}).
			Find(&rows).Error; err != nil {
			return "", "segment_lookup_failed"
		}
		var found string
		for _, row := range rows {
			evidence, usable := decodeIPSegmentRawEvidence(row.RawJSON)
			var begin, end string
			if !usable || !sameIPv4SegmentAddress(row.LookupKey, ip) ||
				json.Unmarshal(evidence.BeginIP, &begin) != nil || json.Unmarshal(evidence.EndIP, &end) != nil ||
				!evidence.matches(row.LookupKey, begin, end) {
				return "", "segment_untrusted"
			}
			bounds, ok := parseBlacklistIPv4Range(begin + "-" + end)
			got := bounds.String()
			if !ok || !bounds.contains(addr) || (found != "" && found != got) {
				return "", "segment_untrusted"
			}
			found = got
		}
		if found != "" {
			return found, ""
		}
	}
	if !db.Migrator().HasTable(entity.TableNameIPSegment) {
		return "", "segment_not_cached"
	}
	v, _ := ipv4ToUint32(ip)
	candidates := db.Model(&entity.IPSegment{}).Where("begin_ip <= ? AND end_ip >= ? AND source IN ?", v, v, []string{"", "standard"}).Session(&gorm.Session{})
	var rows []entity.IPSegment
	if err := candidates.Select("begin_ip, end_ip, begin_text, end_text, sample_ip, raw_json").
		Where("(end_ip - begin_ip) = (?)", candidates.Select("MIN(end_ip - begin_ip)")).Find(&rows).Error; err != nil {
		return "", "segment_lookup_failed"
	}
	var found string
	for _, row := range rows {
		bounds, ok := parseBlacklistIPv4Range(row.BeginText + "-" + row.EndText)
		sample, sampleErr := netip.ParseAddr(strings.TrimSpace(row.SampleIP))
		rawLo, _ := ipv4ToUint32(row.BeginText)
		rawHi, _ := ipv4ToUint32(row.EndText)
		got := bounds.String()
		if !ok || !bounds.contains(addr) || sampleErr != nil || sample.Zone() != "" || !bounds.contains(sample) ||
			rawLo != row.BeginIP || rawHi != row.EndIP || (found != "" && found != got) {
			return "", "segment_untrusted"
		}
		// Previously projected rows may already contain contradictory raw data.
		// Their numeric/text columns alone cannot make that evidence trustworthy.
		if strings.TrimSpace(row.RawJSON) != "" {
			evidence, usable := decodeIPSegmentRawEvidence(row.RawJSON)
			if !usable || !evidence.matches(row.SampleIP, row.BeginText, row.EndText) {
				return "", "segment_untrusted"
			}
		}
		found = got
	}
	if found == "" {
		return "", "segment_not_cached"
	}
	return found, ""
}
