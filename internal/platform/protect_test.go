package platform

import (
	"bytes"
	"testing"
)

func TestProtectRoundTrip(t *testing.T) {
	plain := []byte(`{"sessions":[]}`)
	sealed, err := Protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Unprotect(sealed)
	if err != nil || !bytes.Equal(back, plain) {
		t.Fatalf("%q %v", back, err)
	}
}
