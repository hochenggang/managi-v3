// Package sshpool - 命令执行：在池里的连接上跑一条非交互命令并取回输出。
// 与终端通道无关：不申请 PTY，因此 stdout/stderr 天然分离，无需解析转义序列。
package sshpool

import (
	"bytes"
	"context"
	"strings"

	"managi/internal/model"
)

// Execute 在指定连接上执行命令，返回按行拆分的 stdout 与 stderr。
// 调用方负责 Get/Release；Execute 本身不释放连接。
// 支持 ctx 取消，客户端断开时终止 SSH 命令执行。
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

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

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
