// Package handler - SSH 命令执行端点。
// 对应 v2 routers.py 的 execute_single_command / test_ssh_connection / batch_execute_commands。
// 设计见 ../design-v3.md §4.3（并发模型）与 §6.2（重试幂等）。
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"managi/internal/config"
	"managi/internal/model"
	"managi/internal/sshpool"
)

// maxRequestBodySize 限制请求体大小（修复 B12：防止超大请求体导致 OOM）。
const maxRequestBodySize = 10 << 20 // 10MB

// maxCmdsCount 单次请求最大命令条数。
const maxCmdsCount = 100

// maxCmdLength 单条命令最大字符数。
const maxCmdLength = 4096

// testHandler POST /api/ssh/test
// 请求体: {node, cmds}  响应: CmdsTestResult
//
//nolint:unparam // cfg 保留供未来扩展，并与同包 handler 签名一致
func testHandler(pool *sshpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// M5：仅允许 POST，其他方法返回 405
		if r.Method != http.MethodPost {
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// 修复 B12：限制请求体大小
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		var req struct {
			Node model.Node `json:"node"`
			Cmds []string   `json:"cmds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateNode(req.Node); err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateCmds(req.Cmds); err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		result := executeSingle(r.Context(), pool, req.Node, req.Cmds)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// batchHandler POST /api/ssh/batch
// 请求体: {nodes, cmds}  响应: []CmdsTestResult
// v3：errgroup 并发执行，SetLimit 控制并发数。
//
//nolint:unparam // cfg 保留供未来扩展，并与同包 handler 签名一致
func batchHandler(pool *sshpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// M5：仅允许 POST，其他方法返回 405
		if r.Method != http.MethodPost {
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// 修复 B12：限制请求体大小
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		var req model.BatchCmdRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, node := range req.Nodes {
			if err := validateNode(node); err != nil {
				writeJSONError(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if err := validateCmds(req.Cmds); err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		results := make([]model.CmdsTestResult, len(req.Nodes))

		// errgroup 提供并发上限与 ctx 取消语义；单节点失败不取消其他节点
		// （由 results[i].Success 表达失败），故闭包仍 return nil。
		g, ctx := errgroup.WithContext(r.Context())
		g.SetLimit(10) // 并发上限
		// 修复 B32：Go 1.22+ 循环变量每次迭代是新变量，无需 i,node := i,node
		for i, node := range req.Nodes {
			g.Go(func() error {
				results[i] = executeSingle(ctx, pool, node, req.Cmds)
				return nil
			})
		}
		_ = g.Wait()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(results)
	}
}

// executeSingle 单节点命令执行，连接用完 release（修正 v2：release 不关闭）。
// 修复 B11：接收 ctx，客户端断开时终止 SSH 命令执行。
func executeSingle(ctx context.Context, pool *sshpool.Pool, node model.Node, cmds []string) model.CmdsTestResult {
	start := time.Now()
	output, errs, err := pool.Execute(ctx, node, cmds)
	// 连接失败时 err 非 nil，应视为失败（修正忽略 err 的缺陷）
	success := err == nil && len(errs) == 0
	allErrors := errs
	if err != nil {
		allErrors = append([]string{err.Error()}, errs...)
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

// validateNode 校验节点字段完整性，防止无效输入触发下游无用连接。
func validateNode(n model.Node) error {
	if n.Host == "" {
		return fmt.Errorf("node.host is required")
	}
	if n.Port < 1 || n.Port > 65535 {
		return fmt.Errorf("node.port must be 1-65535, got %d", n.Port)
	}
	if n.Username == "" {
		return fmt.Errorf("node.username is required")
	}
	return nil
}

// validateCmds 校验命令列表长度与单条命令长度。
func validateCmds(cmds []string) error {
	if len(cmds) == 0 {
		return fmt.Errorf("cmds is required")
	}
	if len(cmds) > maxCmdsCount {
		return fmt.Errorf("too many commands: %d, max %d", len(cmds), maxCmdsCount)
	}
	for i, c := range cmds {
		if len(c) > maxCmdLength {
			return fmt.Errorf("command %d too long: %d chars, max %d", i, len(c), maxCmdLength)
		}
	}
	return nil
}
