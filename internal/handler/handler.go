package handler

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

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

	// 解析
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
		return h.handleData(req)
	case protocol.TypeClose:
		return h.handleClose(req)
	default:
		return nil
	}
}

func (h *Handler) handleConnect(req *protocol.Request, from *net.UDPAddr) []byte {
	network := req.NetworkString()
	target := req.TargetAddr()

	h.log(logInfo, "连接: %s %s (ID:%d)", network, target, req.ReqID)

	// 建立连接
	conn, err := net.DialTimeout(network, target, 10*time.Second)
	if err != nil {
		h.log(logDebug, "连接失败: %s - %v", target, err)
		return h.response(req.ReqID, 0x01, nil) // 失败
	}

	// 保存连接
	c := &Conn{
		ID:         req.ReqID,
		Target:     conn,
		ClientAddr: from,
		Network:    network,
		LastActive: time.Now(),
	}
	h.conns.Store(req.ReqID, c)

	// 如果有初始数据，发送
	if len(req.Data) > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_, _ = conn.Write(req.Data)
	}

	// 启动读取循环
	go h.readLoop(c)

	return h.response(req.ReqID, 0x00, nil) // 成功
}

func (h *Handler) handleData(req *protocol.Request) []byte {
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
	return h.response(req.ReqID, 0x00, nil)
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
		addr := c.ClientAddr
		c.mu.Unlock()

		// 发送响应
		resp := h.response(c.ID, protocol.TypeData, buf[:n])
		if resp != nil && h.sender != nil && addr != nil {
			_ = h.sender(resp, addr)
		}
	}
}

func (h *Handler) response(reqID uint32, status byte, data []byte) []byte {
	plain := protocol.BuildResponse(reqID, status, data)
	encrypted, err := h.crypto.Encrypt(plain)
	if err != nil {
		return nil
	}
	return encrypted
}

func (h *Handler) closeConn(reqID uint32) {
	if v, ok := h.conns.LoadAndDelete(reqID); ok {
		c := v.(*Conn)
		if c.Target != nil {
			_ = c.Target.Close()
		}
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
