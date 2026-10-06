// Package sshpool - 命令执行：在池里的连接上跑一条非交互命令并取回输出。
// 与终端通道无关：不申请 PTY，因此 stdout/stderr 天然分离，无需解析转义序列。
package sshpool

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"managi/internal/model"
)

// MaxExecOutputBytes 单路（stdout/stderr 各自）收集上限：命令输出像 cat 大文件、
// yes 这类无限流时，无上限缓冲会把整个进程拖到 OOM，池与终端会话一起陪葬。
const MaxExecOutputBytes = 4 << 20

// Execute 在指定连接上执行命令，返回按行拆分的 stdout 与 stderr。
// 调用方负责 Get/Release；Execute 本身不释放连接。
// 支持 ctx 取消，客户端断开或调用方超时时终止 SSH 命令执行。
// 单路输出超过 MaxExecOutputBytes 的部分丢弃，并在 output 末尾追加一行截断提示。
func (p *Pool) Execute(ctx context.Context, node model.Node, cmds []string) (output []string, errs []string, err error) {
	if len(cmds) == 0 {
		return nil, nil, nil
	}
	conn, err := p.Get(node)
	if err != nil {
		return nil, nil, err
	}
	defer p.Release(conn)

	session, err := conn.client.NewSession()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = session.Close() }()

	stdout := newLimitedBuffer(MaxExecOutputBytes)
	stderr := newLimitedBuffer(MaxExecOutputBytes)
	session.Stdout = stdout
	session.Stderr = stderr

	// ctx 取消时关闭 session 终止命令执行
	if err := session.Start(strings.Join(cmds, "\n")); err != nil {
		return nil, nil, err
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- session.Wait()
	}()
	select {
	case runErr := <-waitCh:
		output = splitLines(stdout.String())
		errs = splitLines(stderr.String())
		// 截断提示放在 output：它是展示限制而非命令失败，混进 errs 会把 success 判成 false
		if stdout.truncated {
			output = append(output, truncateNote("stdout"))
		}
		if stderr.truncated {
			output = append(output, truncateNote("stderr"))
		}
		if runErr != nil && len(errs) == 0 {
			errs = []string{runErr.Error()}
		}
		return output, errs, nil
	case <-ctx.Done():
		_ = session.Close() // 终止 Wait
		<-waitCh            // 等待 goroutine 退出
		return nil, nil, ctx.Err()
	}
}

// limitedBuffer 有上限的输出收集器：先到 limit 的部分保留，其余丢弃并置 truncated。
// Write 永远报告成功（含被丢弃的字节）：ssh 侧复制协程因此继续把远端输出读干净，
// 不会因收集器报错提前断流——我们要的是丢字节，不是断命令。
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	switch {
	case room >= len(p):
		b.buf.Write(p)
	case room > 0:
		b.buf.Write(p[:room])
		b.truncated = true
	default:
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

// truncateNote 生成截断提示行（带上限值，常量改了文案自动跟随）。
func truncateNote(stream string) string {
	return fmt.Sprintf("[managi] %s 输出超过 %dMB，已截断", stream, MaxExecOutputBytes>>20)
}

// splitLines 按行拆分命令输出：保留中间空行（cat/df/awk 等输出里的空行是内容的一部分，
// 丢掉会让前端显示串行错位），只去掉行尾 \r 与末尾换行产生的空尾行。
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	raw := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		out = append(out, strings.TrimRight(line, "\r"))
	}
	// 尾部空行不携带信息（多数命令以换行收尾），整体即空 ⇒ 返回空切片
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
