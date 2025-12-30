package web

import "testing"

func TestParseCIDRAllowlist(t *testing.T) {
	allowlist, err := ParseCIDRAllowlist("192.0.2.0/24, 2001:db8::/32, 127.0.0.1, localhost")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowlist == nil {
		t.Fatal("expected allowlist to be parsed")
	}
	if !allowlist.Allows("192.0.2.10") {
		t.Fatal("expected allowlist to allow IPv4 CIDR")
	}
	if !allowlist.Allows("2001:db8::1") {
		t.Fatal("expected allowlist to allow IPv6 CIDR")
	}
	if !allowlist.Allows("127.0.0.1") || !allowlist.Allows("::1") {
		t.Fatal("expected allowlist to include localhost")
	}
	if allowlist.Allows("198.51.100.1") {
		t.Fatal("expected allowlist to deny non-listed IP")
	}
}

func TestParseCIDRAllowlistInvalid(t *testing.T) {
	allowlist, err := ParseCIDRAllowlist("not-a-cidr")
	if err == nil {
		t.Fatal("expected error for invalid allowlist")
	}
	if allowlist != nil {
		t.Fatal("expected nil allowlist on error")
	}
}

func TestParseCIDRAllowlistEmpty(t *testing.T) {
	allowlist, err := ParseCIDRAllowlist(" , ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowlist != nil {
		t.Fatal("expected nil allowlist for empty input")
	}
}
