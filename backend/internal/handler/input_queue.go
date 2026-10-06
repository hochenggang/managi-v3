// Package handler - 每通道输入队列。
//
// 读循环是整条 WS 连接共用的一条协程，而通道上的操作（PTY stdin 写、上传落盘、
// sftp 目录调用）都可能在远端卡住：原地执行意味着一个卡死的远端冻住整条连接。
// 队列把两者拆开：读循环只做非阻塞入队，每通道一条工作协程按序执行这些阻塞操作。
//
// 容量按字节预算计（帧按载荷 + 固定开销，控制动词按固定开销）：帧长不定，
// 按条数封顶会漏掉大帧的内存；控制帧洪泛同样受预算约束。预算与前端窗口的关系
// 见 stream.go 的 inputWindow。队列满属于对端违规（前端按窗口节流不可能触发），
// 策略由各通道自定：PTY 丢弃并出声，SFTP 终止通道。
package handler

import (
	"sync"

	"managi/internal/wire"
)

// 队列事项的两种形态。
type opKind uint8

const (
	opFrame opKind = iota
	opControl
)

// 单条事项的固定记账开销（近似其队列内真实内存：chanOp 结构 + envelope 原文）。
// 精确值不重要，重要的是大量小帧与控制帧同样撞得上预算。
const (
	frameOpCost   = 64
	controlOpCost = 128
)

// chanOp 队列里的一件事：数据帧或控制动词，由 kind 区分。
type chanOp struct {
	kind  opKind
	frame wire.Frame
	env   wsEnvelope
}

// inputQueue 每通道的有界输入队列。
// push 非阻塞（满返回 false，策略归调用方）；pop 阻塞直到有事项或队列关闭；
// 关闭后未消费的事项直接作废——通道已死，无需再为窗口补偿。
type inputQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	ops    []chanOp
	used   int
	budget int
	closed bool
}

func newInputQueue(budget int) *inputQueue {
	q := &inputQueue{budget: budget}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// pushFrame 尝试入队一帧数据；队列满返回 false。
func (q *inputQueue) pushFrame(f wire.Frame) bool {
	return q.push(chanOp{kind: opFrame, frame: f}, frameOpCost+len(f.Payload))
}

// pushControl 尝试入队一个控制动词；队列满返回 false。
func (q *inputQueue) pushControl(env wsEnvelope) bool {
	return q.push(chanOp{kind: opControl, env: env}, controlOpCost)
}

func (q *inputQueue) push(op chanOp, cost int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.used+cost > q.budget {
		return false
	}
	q.ops = append(q.ops, op)
	q.used += cost
	q.cond.Signal()
	return true
}

// pop 取出一件事项（阻塞）。队列关闭后返回 ok=false，未消费的事项作废。
func (q *inputQueue) pop() (chanOp, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.ops) == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed {
		return chanOp{}, false
	}
	// 只推进切片头，不搬运内存；顺序即入队顺序，是上传字节序与 resize/输入次序的依据
	op := q.ops[0]
	q.ops = q.ops[1:]
	if op.kind == opFrame {
		q.used -= frameOpCost + len(op.frame.Payload)
	} else {
		q.used -= controlOpCost
	}
	return op, true
}

// clear 丢弃全部未消费事项，返回其中数据帧的载荷字节数。
// 上传溢出终止时用它把窗口补偿出去：客户端正等在对端 ack 上，
// 作废的数据不衰减窗口，对端就会永远停在等待里。
func (q *inputQueue) clear() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	var dropped int
	for _, op := range q.ops {
		if op.kind == opFrame {
			dropped += len(op.frame.Payload)
		}
	}
	q.ops = nil
	q.used = 0
	return dropped
}

// close 关闭队列（幂等并作废余项）：唤醒 pop 让工作协程退出。
func (q *inputQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.ops = nil
	q.used = 0
	q.cond.Broadcast()
}
