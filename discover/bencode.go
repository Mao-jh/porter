// bencode.go BitTorrent bencode 编解码（BEP 3，纯标准库）。
// 重点：解析保留每个节点的原始字节区间（raw），使 info 字典的原始 bencode
// 可精确定位——info_hash = SHA1(info 原始字节)，键序/空白必须逐字节一致。
package discover

import (
	"fmt"
	"strconv"
)

// bnode 解码节点。
type bnode struct {
	kind byte // 'i' 整数 / 's' 字节串 / 'l' 列表 / 'd' 字典
	i    int64
	s    []byte
	list []*bnode
	dict []*bpair   // 有序键值对（bencode 字典按字典序排序，但保留顺序更稳）
	raw  []byte     // 该节点的完整原始字节（含类型标记与结束符）
}

// bpair 字典键值对。
type bpair struct {
	k []byte
	v *bnode
}

// bdecode 解码 data 并返回根节点。
func bdecode(data []byte) (*bnode, error) {
	n, end, err := bdecodeAt(data, 0)
	if err != nil {
		return nil, err
	}
	if end != len(data) {
		return nil, fmt.Errorf("bencode: 尾部多余 %d 字节", len(data)-end)
	}
	return n, nil
}

func bdecodeAt(data []byte, pos int) (*bnode, int, error) {
	if pos >= len(data) {
		return nil, 0, fmt.Errorf("bencode: 意外 EOF")
	}
	start := pos
	switch data[pos] {
	case 'i':
		end := pos + 1
		for end < len(data) && data[end] != 'e' {
			end++
		}
		if end >= len(data) {
			return nil, 0, fmt.Errorf("bencode: 整数未终止")
		}
		v, err := strconv.ParseInt(string(data[pos+1:end]), 10, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("bencode: 整数非法 %q", data[pos+1:end])
		}
		return &bnode{kind: 'i', i: v, raw: data[start : end+1]}, end + 1, nil
	case 'l':
		n := &bnode{kind: 'l', raw: data[start:start]}
		pos++
		for {
			if pos >= len(data) {
				return nil, 0, fmt.Errorf("bencode: 列表未终止")
			}
			if data[pos] == 'e' {
				n.raw = data[start : pos+1]
				return n, pos + 1, nil
			}
			child, next, err := bdecodeAt(data, pos)
			if err != nil {
				return nil, 0, err
			}
			n.list = append(n.list, child)
			pos = next
		}
	case 'd':
		n := &bnode{kind: 'd', raw: data[start:start]}
		pos++
		for {
			if pos >= len(data) {
				return nil, 0, fmt.Errorf("bencode: 字典未终止")
			}
			if data[pos] == 'e' {
				n.raw = data[start : pos+1]
				return n, pos + 1, nil
			}
			// 键：字节串
			key, next, err := bdecodeAt(data, pos)
			if err != nil || key.kind != 's' {
				if err == nil {
					err = fmt.Errorf("bencode: 字典键非字节串")
				}
				return nil, 0, err
			}
			val, next2, err := bdecodeAt(data, next)
			if err != nil {
				return nil, 0, err
			}
			n.dict = append(n.dict, &bpair{k: key.s, v: val})
			pos = next2
		}
	default:
		// 字节串 <len>:<bytes>
		colon := pos
		for colon < len(data) && data[colon] != ':' {
			colon++
		}
		if colon >= len(data) {
			return nil, 0, fmt.Errorf("bencode: 字节串未终止")
		}
		n, err := strconv.Atoi(string(data[pos:colon]))
		if err != nil || n < 0 {
			return nil, 0, fmt.Errorf("bencode: 长度非法 %q", data[pos:colon])
		}
		end := colon + 1 + n
		if end > len(data) {
			return nil, 0, fmt.Errorf("bencode: 字节串越界")
		}
		return &bnode{kind: 's', s: data[colon+1 : end], raw: data[start:end]}, end, nil
	}
}

// dictGet 字典按键取节点；key 缺失或根非字典返回 nil。
func (n *bnode) dictGet(key string) *bnode {
	if n == nil || n.kind != 'd' {
		return nil
	}
	for _, p := range n.dict {
		if string(p.k) == key {
			return p.v
		}
	}
	return nil
}

// asString 节点转字节串。
func (n *bnode) asString() []byte {
	if n == nil || n.kind != 's' {
		return nil
	}
	return n.s
}

// asInt 节点转整数。
func (n *bnode) asInt() int64 {
	if n == nil || n.kind != 'i' {
		return 0
	}
	return n.i
}
