package wire

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecode 覆盖正常帧与各类截断输入。
// 解码器直接面对浏览器发来的字节，长度不足必须报错而不是 panic。
func TestDecode(t *testing.T) {
	payload := []byte("hello 终端")
	f := Frame{Chan: 7, Flags: FlagEnd, Payload: payload}

	got, err := Decode(f.Bytes())
	require.NoError(t, err)
	assert.Equal(t, uint32(7), got.Chan)
	assert.Equal(t, FlagEnd, got.Flags)
	assert.Equal(t, payload, got.Payload)

	// 恰好只有帧头：合法的空载荷帧（FlagEnd 通知就这么发）
	got, err = Decode(Frame{Chan: 1, Flags: FlagEnd}.Bytes())
	require.NoError(t, err)
	assert.Empty(t, got.Payload)

	for _, short := range []int{0, 1, HeaderLen - 1} {
		_, err = Decode(f.Bytes()[:short])
		require.ErrorIs(t, err, ErrShortFrame, "长度 %d 必须报帧头不完整", short)
	}
}

// TestAppendTo 验证追加编码不破坏已有内容，且帧头按 [chan:4][flags:2] 落位——
// 这两个偏移量是前后端共用的唯一约定，写错了前端只会看到莫名的乱码。
func TestAppendTo(t *testing.T) {
	buf := []byte("prefix")
	buf = Frame{Chan: 1, Flags: FlagEnd, Payload: []byte{0x00, 0xFF, 0x80}}.AppendTo(buf)

	require.Len(t, buf, len("prefix")+HeaderLen+3)
	assert.Equal(t, "prefix", string(buf[:len("prefix")]))
	assert.Equal(t, uint32(1), binary.BigEndian.Uint32(buf[6:10]))
	assert.Equal(t, uint16(FlagEnd), binary.BigEndian.Uint16(buf[10:12]))
	// 0x80 不是任何 UTF-8 字符的起始字节：原样保留才说明数据面没做文本转换
	assert.Equal(t, []byte{0x00, 0xFF, 0x80}, buf[len(buf)-3:])
}

// TestRoundTripBinary 任意二进制（含非法 UTF-8）必须逐字节往返。
func TestRoundTripBinary(t *testing.T) {
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	got, err := Decode(Frame{Chan: 9, Payload: data}.Bytes())
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, got.Payload), "载荷被改写，数据面不能再做文本转换")
}
