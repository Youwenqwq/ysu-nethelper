package cas

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"io"
)

// AES_CHARS 是认证网关前端生成随机串使用的字符集。
const aesChars = "ABCDEFGHJKMNPQRSTWXYZabcdefhijkmnprstwxyz2345678"

// randomString 从 aesChars 中均匀随机取 n 个字符（拒绝采样，无模偏倚）。
// 加密上下文里随机性应当不可预测，调用方传 crypto/rand.Reader。
func randomString(r io.Reader, n int) (string, error) {
	buf := make([]byte, n)
	out := make([]byte, n)
	// len(aesChars)=49，拒绝 >= 196(=49*4) 的字节保证均匀
	const limit = 256 - 256%len(aesChars)
	filled := 0
	for filled < n {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", fmt.Errorf("cas: rng failure: %w", err)
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out[filled] = aesChars[int(b)%len(aesChars)]
			filled++
			if filled == n {
				break
			}
		}
	}
	return string(out), nil
}

// pkcs7Pad 对数据做 PKCS7 填充。
func pkcs7Pad(data []byte, blockSize int) []byte {
	n := blockSize - len(data)%blockSize
	pad := make([]byte, n)
	for i := range pad {
		pad[i] = byte(n)
	}
	return append(data, pad...)
}

// EncryptPassword 与认证网关前端等价的 AES-CBC 密码加密：
//  1. 密码前拼接 64 字节随机前缀（规避明文短串特征）；
//  2. 以登录页 pwdEncryptSalt 为 key（UTF-8，长度须为 16/24/32）；
//  3. 16 字节随机串为 IV（UTF-8）；
//  4. AES-CBC + PKCS7，输出 Base64。
//
// r 为随机源，生产传 crypto/rand.Reader；测试可注入确定性源。
func EncryptPassword(password, salt string, r io.Reader) (string, error) {
	key := []byte(salt)
	if l := len(key); l != 16 && l != 24 && l != 32 {
		return "", fmt.Errorf("%w: unexpected pwdEncryptSalt length: %d bytes", ErrProtocol, l)
	}
	prefix, err := randomString(r, 64)
	if err != nil {
		return "", err
	}
	ivStr, err := randomString(r, 16)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	data := pkcs7Pad([]byte(prefix+password), aes.BlockSize)
	ct := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, []byte(ivStr)).CryptBlocks(ct, data)
	return base64.StdEncoding.EncodeToString(ct), nil
}
