package provider

import "testing"

func TestContentVolumeName(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool // true if a and b should produce the same volume name
	}{
		{
			name: "identical URLs match",
			a:    "https://github.com/devsy-org/devsy",
			b:    "https://github.com/devsy-org/devsy",
			want: true,
		},
		{
			name: "case differs but resolves the same",
			a:    "https://github.com/devsy-org/devsy",
			b:    "https://github.com/Devsy-Org/Devsy",
			want: true,
		},
		{
			name: "trailing .git suffix resolves the same",
			a:    "https://github.com/devsy-org/devsy",
			b:    "https://github.com/devsy-org/devsy.git",
			want: true,
		},
		{
			name: "trailing slash resolves the same",
			a:    "https://github.com/devsy-org/devsy",
			b:    "https://github.com/devsy-org/devsy/",
			want: true,
		},
		{
			name: "different repositories do not match",
			a:    "https://github.com/devsy-org/devsy",
			b:    "https://github.com/devsy-org/devpod",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ContentVolumeName(tc.a) == ContentVolumeName(tc.b)
			if got != tc.want {
				t.Errorf(
					"ContentVolumeName(%q) == ContentVolumeName(%q) = %v, want %v",
					tc.a, tc.b, got, tc.want,
				)
			}
		})
	}
}

func TestContentVolumeName_Deterministic(t *testing.T) {
	repo := "https://github.com/devsy-org/devsy"
	first := ContentVolumeName(repo)
	second := ContentVolumeName(repo)
	if first != second {
		t.Errorf("ContentVolumeName(%q) is not deterministic: %q != %q", repo, first, second)
	}
	if first == "" {
		t.Error("ContentVolumeName returned an empty string")
	}
}

func TestContentVolumeName_HasDevsyContentPrefix(t *testing.T) {
	got := ContentVolumeName("https://github.com/devsy-org/devsy")
	const wantPrefix = "devsy-content-"
	if len(got) <= len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Errorf("ContentVolumeName() = %q, want prefix %q", got, wantPrefix)
	}
}
