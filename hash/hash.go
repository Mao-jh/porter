// Package hash 提供流式哈希计算（MD5/SHA1/SHA256）。
// 关键：流式处理，绝不将全文件读入内存（§2 决策「校验」；H-2 稳态内存）。
package hash

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"strings"
)

// Algorithm 哈希算法标识。
type Algorithm string

const (
	MD5    Algorithm = "md5"
	SHA1   Algorithm = "sha1"
	SHA256 Algorithm = "sha256"
)

// New 根据算法名构造流式 hash 计算器。空算法名显式报错（由调用方决定跳过校验）。
func New(algo Algorithm) (hash.Hash, error) {
	switch strings.ToLower(string(algo)) {
	case "md5":
		return md5.New(), nil
	case "sha1":
		return sha1.New(), nil
	case "sha256":
		return sha256.New(), nil
	case "":
		return nil, errors.New("hash: empty algorithm (caller should skip verify instead)")
	default:
		return nil, errors.New("hash: unsupported algorithm")
	}
}

// Sum 从 r 流式读取全部内容并计算哈希十六进制字符串。
// 内存占用 = 固定 read buffer，与文件大小无关。
func Sum(r io.Reader, algo Algorithm) (string, error) {
	h, err := New(algo)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 64<<10) // 64 KiB 固定
	if _, err := io.CopyBuffer(h, r, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
