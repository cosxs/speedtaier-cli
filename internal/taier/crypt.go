package taier

import (
	"crypto/des"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func token(imei, ts string) string {
	return md5hex([]byte("model=Android&imei=" + imei + "&stime=" + ts))
}

func newIMEI() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "TS" + strings.ToUpper(hex.EncodeToString(b[:]))
}

var officialDESKey = func() []byte {
	masked := []byte{0xC1, 0xD2, 0x84, 0xE5, 0x86, 0x81, 0x80, 0xFB}
	for i := range masked {
		masked[i] ^= 0xA5
	}
	return masked
}()

func decryptServerList(hexData []byte) ([]byte, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(string(hexData)))
	if err != nil {
		return nil, fmt.Errorf("节点库不是合法的十六进制数据: %w", err)
	}
	block, err := des.NewCipher(officialDESKey)
	if err != nil {
		return nil, fmt.Errorf("内置 DES 密钥无效: %w", err)
	}
	if len(raw) == 0 || len(raw)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("密文长度 %d 不是块大小的整数倍", len(raw))
	}
	out := make([]byte, len(raw))
	for i := 0; i < len(raw); i += block.BlockSize() {
		block.Decrypt(out[i:i+block.BlockSize()], raw[i:i+block.BlockSize()])
	}
	return pkcs5Unpad(out)
}

func pkcs5Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("解密结果为空")
	}
	pad := int(data[len(data)-1])
	if pad < 1 || pad > 8 || pad > len(data) {
		return nil, fmt.Errorf("PKCS#5 填充无效")
	}
	return data[:len(data)-pad], nil
}
