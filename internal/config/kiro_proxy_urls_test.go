package config

import (
	"reflect"
	"testing"
)

func TestKiroProxyURLsDecodeAtNestedAndLegacyPaths(t *testing.T) {
	for name, raw := range map[string][]byte{
		"nested": []byte(`oauth:
  providers:
    kiro:
      proxy-urls: ["http://one.example:8080", "socks5://two.example:1080"]
`),
		"legacy": []byte(`kiro-proxy-urls: ["http://one.example:8080", "socks5://two.example:1080"]
`),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := ParseConfigBytes(raw)
			if err != nil {
				t.Fatalf("ParseConfigBytes: %v", err)
			}
			want := []string{"http://one.example:8080", "socks5://two.example:1080"}
			if !reflect.DeepEqual(cfg.KiroProxyURLs, want) {
				t.Fatalf("KiroProxyURLs = %#v, want %#v", cfg.KiroProxyURLs, want)
			}
		})
	}
}
