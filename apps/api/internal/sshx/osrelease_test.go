package sshx

import (
	"errors"
	"testing"
)

func TestValidateDebian(t *testing.T) {
	cases := []struct {
		name    string
		content string
		major   int
		wantErr bool
	}{
		{"trixie", "PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"13\"\nVERSION_CODENAME=trixie\nID=debian\n", 13, false},
		{"forky", "ID=debian\nVERSION_ID=\"14\"\n", 14, false},
		{"testing without VERSION_ID", "ID=debian\nVERSION_CODENAME=forky\nPRETTY_NAME=\"Debian GNU/Linux forky/sid\"\n", 14, false},
		{"bookworm", "ID=debian\nVERSION_ID=\"12\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n", 0, true},
		{"ubuntu", "ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\n", 0, true},
		{"sid unknown", "ID=debian\nVERSION_CODENAME=sid\n", 0, true},
		{"empty", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ValidateDebian(ParseOSRelease(tc.content))
			if tc.wantErr {
				if !errors.Is(err, ErrUnsupportedOS) {
					t.Fatalf("want ErrUnsupportedOS, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if info.Major != tc.major {
				t.Fatalf("major = %d, want %d", info.Major, tc.major)
			}
		})
	}
}

func TestParseOSReleaseQuoting(t *testing.T) {
	osr := ParseOSRelease("# comment\nNAME='Debian'\nPRETTY_NAME=\"A \\\"quoted\\\" name\"\n")
	if osr["NAME"] != "Debian" || osr["PRETTY_NAME"] != `A "quoted" name` {
		t.Fatalf("bad parse: %#v", osr)
	}
}
