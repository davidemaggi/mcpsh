package ipc

import "testing"

func TestNewer(t *testing.T) {
	b := func(v, exe string, mt int64) BuildInfo { return BuildInfo{Version: v, Exe: exe, ModTime: mt} }
	cases := []struct {
		name           string
		client, daemon BuildInfo
		want           bool
	}{
		{"same build", b("1.2.0", "/x", 5), b("1.2.0", "/x", 5), false},
		{"newer release", b("1.3.0", "/x", 1), b("1.2.9", "/x", 9), true},
		{"older release", b("v1.2.0", "/x", 9), b("1.10.0", "/x", 1), false},
		{"same release, rebuilt later", b("1.2.0", "/y", 9), b("1.2.0", "/x", 5), true},
		{"dev builds by time", b("dev", "/a", 9), b("dev", "/b", 5), true},
		{"older dev build", b("dev", "/a", 4), b("dev", "/b", 5), false},
		{"prerelease suffix", b("1.2.0-rc.1", "/x", 1), b("1.1.0", "/x", 9), true},
	}
	for _, c := range cases {
		if got := Newer(c.client, c.daemon); got != c.want {
			t.Errorf("%s: Newer = %v, want %v", c.name, got, c.want)
		}
	}
}
