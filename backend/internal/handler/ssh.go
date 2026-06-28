// Package handler - SSH 命令执行端点。
// 对应 v2 routers.py 的 execute_single_command / test_ssh_connection / batch_execute_commands。
// 设计见 ../design-v3.md §4.3（并发模型）与 §6.2（重试幂等）。
package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"managi/internal/config"
	"managi/internal/model"
	"managi/internal/sshpool"
)

// testHandler POST /api/ssh/test
// 请求体: {node, cmds}  响应: CmdsTestResult
func testHandler(pool *sshpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Node model.Node   `json:"node"`
			Cmds []string     `json:"cmds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result := executeSingle(pool, req.Node, req.Cmds)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// batchHandler POST /api/ssh/batch
// 请求体: {nodes, cmds}  响应: []CmdsTestResult
// v3：errgroup 并发执行，SetLimit 控制并发数。
func batchHandler(pool *sshpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.BatchCmdRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		results := make([]model.CmdsTestResult, len(req.Nodes))

		g, ctx := errgroup.WithContext(r.Context())
		g.SetLimit(10) // 并发上限
		for i, node := range req.Nodes {
			i, node := i, node
			g.Go(func() error {
				results[i] = executeSingle(pool, node, req.Cmds)
				return nil
			})
		}
		_ = ctx
		_ = g.Wait()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(results)
	}
}

// executeSingle 单节点命令执行，连接用完 release（修正 v2：release 不关闭）。
func executeSingle(pool *sshpool.Pool, node model.Node, cmds []string) model.CmdsTestResult {
	start := time.Now()
	output, errs, err := pool.Execute(node, cmds)
	// 连接失败时 err 非 nil，应视为失败（修正忽略 err 的缺陷）
	success := err == nil && len(errs) == 0
	allErrors := errs
	if err != nil {
		allErrors = append([]string{err.Error()}, errs...)
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
	out := ""
	for i, c := range cmds {
		if i > 0 {
			out += "\n"
		}
		out += c
	}
	return out
}
