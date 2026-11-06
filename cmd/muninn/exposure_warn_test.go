package main

import "testing"

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"localhost", true},
		{"0.0.0.0", false},      // bind-all
		{"::", false},           // bind-all v6
		{"", false},             // empty host → binds all interfaces
		{"192.168.1.10", false}, // LAN
		{"203.0.113.7", false},  // public
		{"muninn.example.com", false},
	}
	for _, c := range cases {
		if got := isLoopbackHost(c.host); got != c.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestAnyNonLoopbackAddr(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
		want  bool
	}{
		{"all loopback", []string{"127.0.0.1:8475", "[::1]:8476"}, false},
		{"bind all", []string{"0.0.0.0:8475"}, true},
		{"lan address", []string{"192.168.1.10:8475"}, true},
		{"empty host binds every interface", []string{":8475"}, true},
		{"one exposed address among many", []string{"127.0.0.1:8475", "0.0.0.0:8750"}, true},
		{"no address at all", nil, false},
		{"unparsable address is ignored", []string{"not-an-address"}, false},
	}
	for _, c := range cases {
		if got := anyNonLoopbackAddr(c.addrs...); got != c.want {
			t.Errorf("%s: anyNonLoopbackAddr(%v) = %v, want %v", c.name, c.addrs, got, c.want)
		}
	}
}
