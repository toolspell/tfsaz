package az

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteRoutePAC(t *testing.T) {
	var out bytes.Buffer
	err := writeRoutePAC(&out, []string{"Z.Example.Invalid.", "a.example.invalid", "z.example.invalid"}, 18181)
	if err != nil {
		t.Fatal(err)
	}
	want := `function FindProxyForURL(url, host) {
  switch (host.toLowerCase().replace(/\.$/, "")) {
    case "a.example.invalid":
    case "z.example.invalid":
      return "SOCKS5 127.0.0.1:18181; PROXY 127.0.0.1:18181";
  }
  return "DIRECT";
}
`
	if out.String() != want {
		t.Fatalf("PAC =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestWriteRoutePACRejectsInvalidInputsBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		host string
		port int
	}{
		{"example.invalid", 0},
		{"example.invalid", 65536},
		{"", 18000},
		{"https://example.invalid", 18000},
		{"example.invalid:42", 18000},
		{"example..invalid", 18000},
		{"example.invalid..", 18000},
		{"example.invalid\n", 18000},
		{"example.K", 18000},
		{"example.invalid\"; return \"DIRECT\";", 18000},
		{"-example.invalid", 18000},
		{strings.Repeat("a", 64) + ".invalid", 18000},
	} {
		var out bytes.Buffer
		if err := writeRoutePAC(&out, []string{"valid.invalid", tc.host}, tc.port); err == nil || out.Len() != 0 {
			t.Errorf("PAC for host %q and port %d wrote %d bytes with error %v", tc.host, tc.port, out.Len(), err)
		}
	}
}
