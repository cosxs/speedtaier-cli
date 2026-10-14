package taier

import (
	"bytes"
	"crypto/des"
	"encoding/hex"
	"testing"
)

func TestDecryptServerList(t *testing.T) {
	plain := []byte(`[{"hostip":"192.0.2.1"}]`)
	block, err := des.NewCipher(officialDESKey)
	if err != nil {
		t.Fatal(err)
	}
	pad := des.BlockSize - len(plain)%des.BlockSize
	padded := append(bytes.Clone(plain), bytes.Repeat([]byte{byte(pad)}, pad)...)
	enc := make([]byte, len(padded))
	for i := 0; i < len(padded); i += des.BlockSize {
		block.Encrypt(enc[i:i+des.BlockSize], padded[i:i+des.BlockSize])
	}

	got, err := decryptServerList([]byte(hex.EncodeToString(enc) + "\n"))
	if err != nil || !bytes.Equal(got, plain) {
		t.Errorf("decryptServerList = %q, %v; want %q", got, err, plain)
	}
	if _, err := decryptServerList([]byte("zz")); err == nil {
		t.Error("invalid hex: want error")
	}
	if _, err := decryptServerList([]byte("0011")); err == nil {
		t.Error("non block-aligned: want error")
	}
}
