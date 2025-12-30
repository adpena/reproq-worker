package web

import (
	"fmt"
	"net/netip"
	"strings"
)

type CIDRAllowlist struct {
	prefixes []netip.Prefix
}

func ParseCIDRAllowlist(raw string) (*CIDRAllowlist, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		if strings.EqualFold(entry, "localhost") {
			prefixes = append(prefixes, netip.MustParsePrefix("127.0.0.1/32"))
			prefixes = append(prefixes, netip.MustParsePrefix("::1/128"))
			continue
		}
		prefix, err := parseAllowlistPrefix(entry)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, prefix)
	}
	if len(prefixes) == 0 {
		return nil, nil
	}
	return &CIDRAllowlist{prefixes: prefixes}, nil
}

func (a *CIDRAllowlist) Allows(host string) bool {
	if a == nil {
		return true
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if trimmed, _, ok := strings.Cut(host, "%"); ok {
		host = trimmed
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, prefix := range a.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func parseAllowlistPrefix(entry string) (netip.Prefix, error) {
	if strings.Contains(entry, "/") {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid metrics allowlist prefix %q", entry)
		}
		return prefix, nil
	}
	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid metrics allowlist address %q", entry)
	}
	bits := 128
	if addr.Is4() {
		bits = 32
	}
	return netip.PrefixFrom(addr, bits), nil
}
