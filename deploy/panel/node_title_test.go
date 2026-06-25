package main

import (
	"strings"
	"testing"
)

func TestNodeBaseNameAndPanelDisplayTitle(t *testing.T) {
	cases := []struct {
		name  string
		in    Config
		base  string
		title string
	}{
		{"empty", Config{}, "Manage", "Manage_ANS"},
		{"old default", Config{PanelTitle: "ANS-GO 管理面板"}, "Manage", "Manage_ANS"},
		{"custom", Config{PanelTitle: "NodeName"}, "NodeName", "NodeName_ANS"},
		{"trim", Config{PanelTitle: "  NodeName  "}, "NodeName", "NodeName_ANS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nodeBaseName(tc.in); got != tc.base {
				t.Fatalf("nodeBaseName()=%q want %q", got, tc.base)
			}
			if got := panelDisplayTitle(tc.in); got != tc.title {
				t.Fatalf("panelDisplayTitle()=%q want %q", got, tc.title)
			}
		})
	}
}

func TestBuildURIsUseNodeShortFragments(t *testing.T) {
	c := Config{
		Domain: "your-domain.com", PanelTitle: "NodeName",
		SSPort: 10001, SSMethod: "2022-blake3-aes-128-gcm",
		AnyTLSPort: 10002, SocksPort: 10003, NaivePort: 10004,
	}
	s := secretData{
		SSKey:      "aaaaaaaaaaaaaaaaaaaaaaaa",
		AnyTLSPass: "atpass",
		SocksUser:  "suser", SocksPass: "spass",
		NaiveUser:  "nuser", NaivePass: "npass",
	}
	uris := buildURIs(c, s)
	wants := map[string]string{
		"ss":     "#NodeName-SS",
		"anytls": "#NodeName-AT",
		"socks":  "#NodeName-SK",
		"naive":  "#NodeName-NV",
	}
	for k, frag := range wants {
		if !strings.Contains(uris[k], frag) {
			t.Fatalf("%s URI %q does not contain %q", k, uris[k], frag)
		}
	}
}

func TestNodeFragmentSanitizesUnsafeCharacters(t *testing.T) {
	got := nodeFragment(Config{PanelTitle: "Node Name#/"}, "AT")
	if got != "Node_Name__-AT" {
		t.Fatalf("nodeFragment()=%q want %q", got, "Node_Name__-AT")
	}
}
