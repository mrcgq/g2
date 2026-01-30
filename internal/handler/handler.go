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
	mu         sync.Mutex
}

// ClientSession 客户端会话（按地址）
type ClientSession struct {
	Addr       *net.UDPAddr
	ARQ        *arq.Conn
	LastActive time.Time
	mu         sync.Mutex
}

// Handler 处理器
type Handler struct {
	crypto   *crypto.Crypto
	logLevel int

	sender   SendFunc
	conns    sync.Map // reqID -> *Conn
	sessions sync.Map // clientAddr string -> *ClientSession
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

// getOrCreateSession 获取或创建客户端会话
func (h *Handler) getOrCreateSession(from *net.UDPAddr) *ClientSession {
	key := from.String()

	if v, ok := h.sessions.Load(key); ok {
		session := v.(*ClientSession)
		session.mu.Lock()
		session.LastActive = time.Now()
		session.mu.Unlock()
		return session
	}

	// 创建新会话
	session := &ClientSession{
		Addr:       from,
		LastActive: time.Now(),
	}

	// 创建该客户端的全局 ARQ
	session.ARQ = arq.New(func(data []byte) error {
		// ARQ 输出 -> 加密 -> 发送
		encrypted, err := h.crypto.Encrypt(data)
		if err != nil {
			return err
		}
		if h.sender != nil {
			return h.sender(encrypted, from)
		}
		return nil
	})

	// 启动 ARQ 数据处理协程
	go h.processARQData(session)

	h.sessions.Store(key, session)
	h.log(logDebug, "创建客户端会话: %s", key)

	return session
}

// processARQData 处理 ARQ 输出的数据
func (h *Handler) processARQData(session *ClientSession) {
	for {
		data, err := session.ARQ.RecvTimeout(time.Second)
		if err != nil {
			if err == arq.ErrTimeout {
				continue
			}
			if err == arq.ErrClosed {
				return
			}
			continue
		}

		// 解析协议
		h.handleProtocolData(data, session.Addr)
	}
}

// handleProtocolData 处理协议数据
func (h *Handler) handleProtocolData(data []byte, from *net.UDPAddr) {
	req, err := protocol.ParseRequest(data)
	if err != nil {
		h.log(logDebug, "解析失败: %v", err)
		return
	}

	switch req.Type {
	case protocol.TypeConnect:
		h.handleConnect(req, from)
	case protocol.TypeData:
		h.handleData(req)
	case protocol.TypeClose:
		h.handleClose(req)
	}
}

// HandlePacket 处理数据包（入口）
func (h *Handler) HandlePacket(data []byte, from *net.UDPAddr) []byte {
	// 解密
	plaintext, err := h.crypto.Decrypt(data)
	if err != nil {
		h.log(logDebug, "解密失败: %v", err)
		return nil // 静默丢弃
	}

	// 获取或创建该客户端的会话
	session := h.getOrCreateSession(from)

	// 把数据交给该客户端的 ARQ 处理
	if err := session.ARQ.OnReceive(plaintext); err != nil {
		h.log(logDebug, "ARQ 处理失败: %v", err)
	}

	// 返回 nil，响应通过 ARQ 异步发送
	return nil
}

func (h *Handler) handleConnect(req *protocol.Request, from *net.UDPAddr) {
	network := req.NetworkString()
	target := req.TargetAddr()

	h.log(logInfo, "连接: %s %s (ID:%d)", network, target, req.ReqID)

	// 建立连接
	conn, err := net.DialTimeout(network, target, 10*time.Second)
	if err != nil {
		h.log(logDebug, "连接失败: %s - %v", target, err)
		h.sendResponse(req.ReqID, 0x01, nil, from) // 失败
		return
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

	// 发送成功响应
	h.sendResponse(req.ReqID, 0x00, nil, from)

	// 启动读取循环
	go h.readLoop(c)
}

func (h *Handler) handleData(req *protocol.Request) {
	v, ok := h.conns.Load(req.ReqID)
	if !ok {
		return
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
}

func (h *Handler) handleClose(req *protocol.Request) {
	h.closeConn(req.ReqID)
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
		h.sendResponse(c.ID, protocol.TypeData, buf[:n], addr)
	}
}

// sendResponse 通过 ARQ 发送响应
func (h *Handler) sendResponse(reqID uint32, status byte, data []byte, to *net.UDPAddr) {
	// 获取客户端会话
	key := to.String()
	v, ok := h.sessions.Load(key)
	if !ok {
		h.log(logDebug, "会话不存在: %s", key)
		return
	}

	session := v.(*ClientSession)

	// 构建响应
	resp := protocol.BuildResponse(reqID, status, data)

	// 通过 ARQ 发送
	if err := session.ARQ.Send(resp); err != nil {
		h.log(logDebug, "ARQ 发送失败: %v", err)
	}
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

		// 清理超时连接
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

		// 清理超时会话
		h.sessions.Range(func(key, value interface{}) bool {
			session := value.(*ClientSession)
			session.mu.Lock()
			idle := now.Sub(session.LastActive)
			session.mu.Unlock()
			if idle > 10*time.Minute {
				session.ARQ.Close()
				h.sessions.Delete(key)
				h.log(logDebug, "清理客户端会话: %s", key)
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
