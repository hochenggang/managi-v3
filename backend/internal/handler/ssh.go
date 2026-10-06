// Package handler - SSH 命令执行端点。
// 对应 v2 routers.py 的 batch_execute_commands。
// 设计见 design-v5.md §4.3（并发模型）与 §6.2（重试幂等）。
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"managi/internal/model"
	"managi/internal/sshpool"
)

// maxRequestBodySize 限制请求体大小（修复 B12：防止超大请求体导致 OOM）。
const maxRequestBodySize = 10 << 20 // 10MB

// 批量执行的两道硬闸，防止一条请求把整个进程拖垮：
//   - maxBatchNodes：10MB 请求体可塞上万节点，逐个拨号会把连接与协程占住很久；
//   - batchExecTimeout：远端命令挂死（如误开 tail -f）时到点终止，不依赖客户端断开。
const (
	maxBatchNodes    = 100
	batchExecTimeout = 15 * time.Minute
)

// batchHandler POST /api/ssh/batch
// 请求体: {nodes, cmds}  响应: []CmdsTestResult
// v3：errgroup 并发执行，SetLimit 控制并发数。
func batchHandler(pool *sshpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.BatchCmdRequest
		if !decodeJSONRequest(w, r, &req) {
			return
		}
		if len(req.Nodes) > maxBatchNodes {
			writeJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("节点数超过上限（最多 %d 个）", maxBatchNodes))
			return
		}
		results := make([]model.CmdsTestResult, len(req.Nodes))

		ctx, cancel := context.WithTimeout(r.Context(), batchExecTimeout)
		defer cancel()

		// errgroup 提供并发上限与 ctx 取消语义；单节点失败不取消其他节点
		// （由 results[i].Success 表达失败），故闭包仍 return nil。
		g, ctx := errgroup.WithContext(ctx)
		g.SetLimit(10) // 并发上限
		// Go 1.22+ 循环变量每次迭代是新变量，无需 i,node := i,node
		for i, node := range req.Nodes {
			g.Go(func() error {
				results[i] = executeSingle(ctx, pool, node, req.Cmds)
				return nil
			})
		}
		_ = g.Wait()

		writeJSON(w, results)
	}
}

// executeSingle 单节点命令执行，连接用完 release（修正 v2：release 不关闭）。
// 接收 ctx，客户端断开或总时限到点都会终止 SSH 命令执行。
func executeSingle(ctx context.Context, pool *sshpool.Pool, node model.Node, cmds []string) model.CmdsTestResult {
	start := time.Now()
	output, errs, err := pool.Execute(ctx, node, cmds)
	// 连接失败时 err 非 nil，应视为失败（修正忽略 err 的缺陷）
	success := err == nil && len(errs) == 0
	allErrors := errs
	if err != nil {
		msg := err.Error()
		// 只有到点才可能是我方超时：客户端断开是 Canceled，保留原始错误
		if errors.Is(err, context.DeadlineExceeded) {
			msg = fmt.Sprintf("命令执行超时（上限 %d 分钟），已终止", batchExecTimeout/time.Minute)
		}
		allErrors = append([]string{msg}, errs...)
	}
	// 确保 JSON 序列化为 [] 而非 null，避免前端 null.join() 崩溃
	if output == nil {
		output = []string{}
	}
	if allErrors == nil {
		allErrors = []string{}
	}
	return model.CmdsTestResult{
		TimeElapsed: time.Since(start).Seconds(),
		Success:     success,
		Output:      output,
		Error:       allErrors,
		Node:        node.Masked(),
		Cmds:        joinCmds(cmds),
	}
}

func joinCmds(cmds []string) string {
	return strings.Join(cmds, "\n")
}
