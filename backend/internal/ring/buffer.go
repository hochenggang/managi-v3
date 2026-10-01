// Package ring 提供只保留最近若干字节的有界缓冲（终端 scrollback 用）。
//
// 抽象边界：调用方只需要知道「追加什么、现在有什么」，不必关心超限怎么丢、
// 字符边界怎么找——这两条正是最容易写错的地方，因此收进一个类型里单独测。
// 本类型不加锁：并发由持有者（终端会话自己的 mu）负责。
package ring

// DefaultMax 未给出有效上限时的容量：256KB，足够回看数百行终端输出。
const DefaultMax = 256 * 1024

// Buffer 尾部追加、头部丢弃的字节缓冲。
type Buffer struct {
	data []byte
	max  int
}

// New 创建一个容量为 max 的缓冲；max 非正时取 DefaultMax
// （零值配置只出现在测试里，此处兜底与 handler 的其他默认值保持一致）。
func New(max int) *Buffer {
	if max <= 0 {
		max = DefaultMax
	}
	return &Buffer{max: max}
}

// Append 追加字节，超限则丢弃头部最旧的部分。
func (b *Buffer) Append(p []byte) {
	if len(p) == 0 {
		return
	}
	b.data = append(b.data, p...)
	if len(b.data) > b.max {
		b.data = append([]byte(nil), b.data[safeCut(b.data, b.max):]...)
	}
}

// Bytes 返回当前内容的副本：不把可变内部切片漏给调用方。
func (b *Buffer) Bytes() []byte {
	out := make([]byte, len(b.data))
	copy(out, b.data)
	return out
}

// Len 当前字节数。
func (b *Buffer) Len() int { return len(b.data) }

// safeCut 计算丢弃到剩 max 字节时的切断位置，并向前推进到下一个字符起始字节。
// 直接按 max 切可能落在多字节字符中间，回放的第一帧便以半个字符开头（前端渲染成乱码）。
// UTF-8 序列至多 4 字节，故最多推进 3 次。
func safeCut(data []byte, max int) int {
	cut := len(data) - max
	for i := 0; i < 3 && cut < len(data) && isContinuation(data[cut]); i++ {
		cut++
	}
	return cut
}

// isContinuation 是否为 UTF-8 续字节（10xxxxxx）。
func isContinuation(c byte) bool { return c&0xC0 == 0x80 }
