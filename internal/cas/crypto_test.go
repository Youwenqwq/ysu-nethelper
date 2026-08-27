package cas

import (
	"bytes"
	"strings"
	"testing"
)

// TestEncryptPasswordFixture 用 PyCryptodome（ysu-sdk 参考实现同源）生成的
// 固定 fixture 校验 AES-CBC 加密与认证网关前端等价：
//
//	key    = salt ("Ab3dFghJkmNprstX")
//	iv     = "WXYZabcdefhijkmn"
//	明文   = 64 字符 prefix + password
//	输出   = Base64(AES-CBC-PKCS7(明文))
//
// 随机源按 aesChars 下标编排，使 Go 侧产生的 prefix/IV 与 fixture 一致。
func TestEncryptPasswordFixture(t *testing.T) {
	const (
		salt     = "Ab3dFghJkmNprstX"
		iv       = "WXYZabcdefhijkmn"
		prefix   = "2345678ABCDEFGHJKMNPQRSTWXYZabcdefhijkmnprstwxyz2345678ABCDEFGAB"
		password = "Test@Password123"
		want     = "+7RllxbRpbD7XwM7NmfkD+fe1CNHm9JxnGbIVl2fIbg0/94V1/hwJGAeytLK0toXlGDtAdEmVmXtSvxSH9OsSIHkD4xZx9q44vGGQdKLYC2F8gK08wvnOmZ1JcyncckK"
	)

	// randomString 取 b%49 作下标且拒绝 >=196 的字节；直接给下标值即可命中。
	var script []byte
	for _, c := range []byte(prefix + iv) {
		idx := strings.IndexByte(aesChars, c)
		if idx < 0 {
			t.Fatalf("fixture char %q not in aesChars", c)
		}
		script = append(script, byte(idx))
	}

	got, err := EncryptPassword(password, salt, bytes.NewReader(script))
	if err != nil {
		t.Fatalf("EncryptPassword: %v", err)
	}
	if got != want {
		t.Fatalf("ciphertext mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestEncryptPasswordRejectsBadSalt(t *testing.T) {
	_, err := EncryptPassword("pw", "too-short", bytes.NewReader(make([]byte, 256)))
	if err == nil {
		t.Fatal("expected error for bad salt length")
	}
}
