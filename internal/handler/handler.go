// internal/handler/handler.go
package handler

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/anthropics/phantom-server/internal/arq"
	"github.com/anthropics/phantom-server/internal/crypto"
	"github.com/anthropics/phantom-server/internal/protocol"
)

// SendFunc 发送函数
type SendFunc func(data []byte, addr *net.UDPAddr) error

// Conn 连接状态
type Conn struct {
	ID         uint32
	Target     net.Conn
	ClientAddr *net.UDPAddr
	Network    string
	LastActive time.Time
	ARQ        *arq.Conn // 新增：ARQ 连接
	mu         sync.Mutex
}

// Handler 处理器
type Handler struct {
	crypto   *crypto.Crypto
	logLevel int

	sender SendFunc
	conns  sync.Map // reqID -> *Conn
}

const (
	logError = 0
	logInfo  = 1
	logDebug = 2
)

// New 创建处理器
func New(c *crypto.Crypto, logLevel string) *Handler {
	level := logInfo
	switch logLevel {
	case "debug":
		level = logDebug
	case "error":
		level = logError
	}

	h := &Handler{
		crypto:   c,
		logLevel: level,
	}

	go h.cleanup()

	return h
}

// SetSender 设置发送函数
func (h *Handler) SetSender(fn SendFunc) {
	h.sender = fn
}

// HandlePacket 处理数据包
func (h *Handler) HandlePacket(data []byte, from *net.UDPAddr) []byte {
	// 解密
	plaintext, err := h.crypto.Decrypt(data)
	if err != nil {
		h.log(logDebug, "解密失败: %v", err)
		return nil // 静默丢弃
	}

	// 首先检查是否是 ARQ 控制包（通过检查已有连接）
	if len(plaintext) >= arq.HeaderSize {
		if handled := h.tryHandleARQ(plaintext, from); handled {
			return nil
		}
	}

	// 解析为协议请求
	req, err := protocol.ParseRequest(plaintext)
	if err != nil {
		h.log(logDebug, "解析失败: %v", err)
		return nil
	}

	// 处理
	switch req.Type {
	case protocol.TypeConnect:
		return h.handleConnect(req, from)
	case protocol.TypeData:
		return h.handleData(req, from)
	case protocol.TypeClose:
		return h.handleClose(req)
	default:
		return nil
	}
}

// tryHandleARQ 尝试处理 ARQ 包
func (h *Handler) tryHandleARQ(data []byte, from *net.UDPAddr) bool {
	// 遍历所有连接，尝试让其 ARQ 处理这个包
	handled := false
	h.conns.Range(func(key, value interface{}) bool {
		c := value.(*Conn)
		c.mu.Lock()
		if c.ARQ != nil && c.ClientAddr.String() == from.String() {
			if err := c.ARQ.OnReceive(data); err == nil {
				c.LastActive = time.Now()
				handled = true
			}
		}
		c.mu.Unlock()
		return !handled // 如果处理成功就停止遍历
	})
	return handled
}

func (h *Handler) handleConnect(req *protocol.Request, from *net.UDPAddr) []byte {
	network := req.NetworkString()
	target := req.TargetAddr()

	h.log(logInfo, "连接: %s %s (ID:%d)", network, target, req.ReqID)

	// 建立连接
	conn, err := net.DialTimeout(network, target, 10*time.Second)
	if err != nil {
		h.log(logDebug, "连接失败: %s - %v", target, err)
		return h.response(req.ReqID, 0x01, nil, from) // 失败
	}

	// 创建 ARQ 连接
	arqConn := arq.New(func(data []byte) error {
		encrypted, err := h.crypto.Encrypt(data)
		if err != nil {
			return err
		}
		if h.sender != nil {
			return h.sender(encrypted, from)
		}
		return nil
	})

	// 保存连接
	c := &Conn{
		ID:         req.ReqID,
		Target:     conn,
		ClientAddr: from,
		Network:    network,
		LastActive: time.Now(),
		ARQ:        arqConn,
	}
	h.conns.Store(req.ReqID, c)

	// 如果有初始数据，发送
	if len(req.Data) > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_, _ = conn.Write(req.Data)
	}

	// 启动读取循环
	go h.readLoop(c)

	return h.response(req.ReqID, 0x00, nil, from) // 成功
}

func (h *Handler) handleData(req *protocol.Request, from *net.UDPAddr) []byte {
	v, ok := h.conns.Load(req.ReqID)
	if !ok {
		return nil
	}

	c := v.(*Conn)
	c.mu.Lock()
	c.LastActive = time.Now()
	c.mu.Unlock()

	if len(req.Data) > 0 {
		_ = c.Target.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := c.Target.Write(req.Data); err != nil {
			h.closeConn(req.ReqID)
		}
	}

	return nil
}

func (h *Handler) handleClose(req *protocol.Request) []byte {
	h.closeConn(req.ReqID)
	return nil
}

func (h *Handler) readLoop(c *Conn) {
	defer h.closeConn(c.ID)

	buf := make([]byte, 32*1024)

	for {
		_ = c.Target.SetReadDeadline(time.Now().Add(5 * time.Minute))
		n, err := c.Target.Read(buf)
		if err != nil {
			if err != io.EOF {
				h.log(logDebug, "读取结束: ID:%d - %v", c.ID, err)
			}
			return
		}

		c.mu.Lock()
		c.LastActive = time.Now()
		arqConn := c.ARQ
		c.mu.Unlock()

		// 通过 ARQ 发送响应
		if arqConn != nil {
			resp := protocol.BuildResponse(c.ID, protocol.TypeData, buf[:n])
			if err := arqConn.Send(resp); err != nil {
				h.log(logDebug, "ARQ 发送失败: ID:%d - %v", c.ID, err)
				return
			}
		}
	}
}

func (h *Handler) response(reqID uint32, status byte, data []byte, from *net.UDPAddr) []byte {
	plain := protocol.BuildResponse(reqID, status, data)

	// 对于连接响应，直接加密返回（不经过 ARQ，因为这是握手响应）
	// 后续数据会通过 ARQ 发送
	encrypted, err := h.crypto.Encrypt(plain)
	if err != nil {
		return nil
	}
	return encrypted
}

func (h *Handler) closeConn(reqID uint32) {
	if v, ok := h.conns.LoadAndDelete(reqID); ok {
		c := v.(*Conn)
		c.mu.Lock()
		if c.Target != nil {
			_ = c.Target.Close()
		}
		if c.ARQ != nil {
			_ = c.ARQ.Close()
		}
		c.mu.Unlock()
		h.log(logInfo, "断开: ID:%d", reqID)
	}
}

func (h *Handler) cleanup() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		h.conns.Range(func(key, value interface{}) bool {
			c := value.(*Conn)
			c.mu.Lock()
			idle := now.Sub(c.LastActive)
			c.mu.Unlock()
			if idle > 5*time.Minute {
				h.closeConn(key.(uint32))
			}
			return true
		})
	}
}

func (h *Handler) log(level int, format string, args ...interface{}) {
	if level > h.logLevel {
		return
	}
	prefix := map[int]string{logError: "[ERROR]", logInfo: "[INFO]", logDebug: "[DEBUG]"}[level]
	fmt.Printf("%s %s %s\n", prefix, time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
