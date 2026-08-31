package update

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.2.0", "0.3.0", true},
		{"0.3.0", "0.3.0", false},
		{"0.3.1", "0.3.0", false},
		{"0.3.0", "1.0.0", true},
		{"1.0.0", "0.9.9", false},
		{"v0.2.0", "v0.10.0", true}, // numeric, not lexicographic
		{"dev", "0.3.0", false},     // unparseable never nags
		{"0.3.0", "", false},
		{"", "0.3.0", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestExpectedSum(t *testing.T) {
	sums := []byte("abc123  unitrise-gate-windows-amd64.exe\ndef456  unitrise-gate-linux-amd64\n")
	if got := expectedSum(sums, "unitrise-gate-windows-amd64.exe"); got != "abc123" {
		t.Errorf("windows sum = %q", got)
	}
	if got := expectedSum(sums, "unitrise-gate-darwin-arm64"); got != "" {
		t.Errorf("missing entry should be empty, got %q", got)
	}
	// sha256sum's binary-mode marker (*name) still matches.
	if got := expectedSum([]byte("aa11 *unitrise-gate-linux-amd64\n"), "unitrise-gate-linux-amd64"); got != "aa11" {
		t.Errorf("starred name = %q", got)
	}
}
