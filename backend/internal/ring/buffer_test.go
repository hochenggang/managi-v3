package ring

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuffer_AppendsWithinCapacity(t *testing.T) {
	b := New(1024)
	b.Append([]byte("hello"))
	assert.Equal(t, "hello", string(b.Bytes()))
	assert.Equal(t, 5, b.Len())
}

func TestBuffer_EmptyAppendIsNoop(t *testing.T) {
	b := New(16)
	b.Append(nil)
	assert.Equal(t, 0, b.Len())
}

func TestBuffer_TruncatesOldest(t *testing.T) {
	b := New(10)
	b.Append([]byte("0123456789"))
	b.Append([]byte("ab"))
	assert.Equal(t, "23456789ab", string(b.Bytes()), "超限应从头丢弃，保留最近的 10 字节")
	assert.LessOrEqual(t, b.Len(), 10)
}

func TestBuffer_TruncateKeepsWholeChars(t *testing.T) {
	b := New(8)
	one := []byte("中") // 3 字节：8 的容量必然把切点落在字符中间
	for i := 0; i < 10; i++ {
		b.Append(one)
	}
	require.LessOrEqual(t, b.Len(), 8)
	assert.True(t, utf8.Valid(b.Bytes()), "截断后的缓冲应是完整字符序列")
}

func TestBuffer_BytesIsCopy(t *testing.T) {
	b := New(64)
	b.Append([]byte("abcd"))
	got := b.Bytes()
	got[0] = 'X'
	assert.Equal(t, "abcd", string(b.Bytes()), "外部改动不该影响缓冲内容")
}

func TestNew_FallsBackToDefault(t *testing.T) {
	assert.Equal(t, DefaultMax, New(0).max)
	assert.Equal(t, DefaultMax, New(-1).max)
}

func TestBuffer_LargeMixOfChars(t *testing.T) {
	b := New(100)
	// 反复追加 3 字节与 4 字节混排的内容：任何一次截断都不能留下残缺字符
	chunk := []byte(strings.Repeat("终端😀", 20))
	for i := 0; i < 50; i++ {
		b.Append(chunk)
		require.LessOrEqual(t, b.Len(), 100)
		require.True(t, utf8.Valid(b.Bytes()), "第 %d 次追加后缓冲区仍是完整字符序列", i)
	}
}
