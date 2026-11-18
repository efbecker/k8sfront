package passwd

import (
	"strings"
	"testing"
)

func TestHashVerify(t *testing.T) {
	h, err := Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("format %s", h)
	}
	if !Verify(h, "correct horse battery") || Verify(h, "correct horse batterY") || Verify("", "x") || Verify("$argon2id$v=19$m=1,t=1,p=1$$", "") {
		t.Fatal("verify")
	}
	h2, _ := Hash("correct horse battery")
	if h == h2 {
		t.Fatal("salt must be random")
	}
}

func TestValidate(t *testing.T) {
	if Validate("short") == nil || Validate(strings.Repeat("x", 300)) == nil || Validate("long enough pw") != nil {
		t.Fatal("policy")
	}
}
