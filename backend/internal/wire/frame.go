// Package wire 定义前后端之间数据面的帧编解码。
//
// 一条 WS 连接上有两种帧：
//   - 文本帧 = 控制面：JSON envelope，一问一答，靠 seq 关联（见 handler 包）。
//   - 二进制帧 = 数据面：本包定义的定长帧头 + 原样字节。
//
// 数据面存在的意义是让 PTY 输出与文件字节不经过「string→JSON」：
// 既省掉转义与再解析的开销，也不会把跨帧边界的半个 UTF-8 字符替换成 U+FFFD。
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// HeaderLen 帧头长度：[chan:4][flags:2]。
// 帧头刻意不含序号：一条 WS 连接是有序流（TCP 保证），同一通道上又同时只跑一路
// 字节流（服务端拒并发），所以「第几片」在服务端和前端都没有消费者。
const HeaderLen = 6

// Flag 帧标志位。
type Flag uint16

// FlagEnd 表示该通道上的这路数据流到此结束（下载的最后一片、会话终止通知）。
// 载荷可为空：仅用于「结束了」这个信号。
const FlagEnd Flag = 1 << 0

// Frame 一帧数据。Payload 直接引用解码时的输入切片，不再拷贝。
type Frame struct {
	Chan    uint32
	Flags   Flag
	Payload []byte
}

// ErrShortFrame 帧头不完整：字节流已错位，这条连接不可再用。
var ErrShortFrame = errors.New("wire: frame shorter than header")

// Decode 解析二进制帧。只校验长度，不解释载荷内容。
func Decode(data []byte) (Frame, error) {
	if len(data) < HeaderLen {
		return Frame{}, fmt.Errorf("%w: got %d bytes, need %d", ErrShortFrame, len(data), HeaderLen)
	}
	return Frame{
		Chan:    binary.BigEndian.Uint32(data[:4]),
		Flags:   Flag(binary.BigEndian.Uint16(data[4:6])),
		Payload: data[HeaderLen:],
	}, nil
}

// AppendTo 把帧编码后追加到 buf 并返回扩展后的切片，供热路径复用底层数组。
func (f Frame) AppendTo(buf []byte) []byte {
	var head [HeaderLen]byte
	binary.BigEndian.PutUint32(head[:4], f.Chan)
	binary.BigEndian.PutUint16(head[4:6], uint16(f.Flags))
	buf = append(buf, head[:]...)
	return append(buf, f.Payload...)
}

// Bytes 编码为独立切片。
func (f Frame) Bytes() []byte {
	return f.AppendTo(make([]byte, 0, HeaderLen+len(f.Payload)))
}

// End 便捷构造一个「流结束」帧。
func End(chanID uint32) Frame {
	return Frame{Chan: chanID, Flags: FlagEnd}
}
