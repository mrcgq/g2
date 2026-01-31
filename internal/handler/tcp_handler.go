//internal/handler/handler.go
package handler

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/anthropics/phantom-server/internal/crypto"
	"github.com/anthropics/phantom-server/internal/protocol"
)

// Conn 表示一个代理连接
type Conn struct {
	ID         uint32
	Target     io.ReadWriteCloser
	ClientAddr *net.UDPAddr
	LastActive time.Time
	Network    byte
}

// Handler 处理代理请求
type Handler struct {
	crypto   *crypto.Crypto
	conns    sync.Map // map[uint32]*Conn
	sender   func(data []byte, addr *net.UDPAddr) error
	logLevel string
	mu       sync.Mutex
}

// TCPHandler 是 Handler 的别名，用于 TCP 传输
type TCPHandler = Handler

// New 创建新的 Handler
func New(c *crypto.Crypto, logLevel string) *Handler {
	h := &Handler{
		crypto:   c,
		logLevel: logLevel,
	}
	go h.cleanupLoop()
	return h
}

// NewTCPHandler 创建新的 TCP Handler
func NewTCPHandler(c *crypto.Crypto, logLevel string) *TCPHandler {
	return New(c, logLevel)
}

// SetSender 设置发送函数
func (h *Handler) SetSender(sender func(data []byte, addr *net.UDPAddr) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sender = sender
}

// HandlePacket 处理收到的数据包
func (h *Handler) HandlePacket(data []byte, from *net.UDPAddr) []byte {
	plaintext, err := h.crypto.Decrypt(data)
	if err != nil {
		h.logDebug("解密失败: %v", err)
		return nil
	}

	if len(plaintext) < 1 {
		return nil
	}

	msgType := plaintext[0]
	switch msgType {
	case protocol.TypeConnect:
		return h.handleConnect(plaintext, from)
	case protocol.TypeData:
		return h.handleData(plaintext, from)
	case protocol.TypeDisconnect:
		return h.handleDisconnect(plaintext, from)
	default:
		h.logDebug("未知消息类型: %d", msgType)
		return nil
	}
}

func (h *Handler) handleConnect(data []byte, from *net.UDPAddr) []byte {
	if len(data) < 7 {
		return nil
	}

	reqID := uint32(data[1])<<24 | uint32(data[2])<<16 | uint32(data[3])<<8 | uint32(data[4])
	network := data[5]
	addrType := data[6]

	var targetAddr string
	var port uint16
	offset := 7

	switch addrType {
	case protocol.AddrIPv4:
		if len(data) < offset+6 {
			return nil
		}
		ip := net.IP(data[offset : offset+4])
		port = uint16(data[offset+4])<<8 | uint16(data[offset+5])
		targetAddr = fmt.Sprintf("%s:%d", ip.String(), port)

	case protocol.AddrIPv6:
		if len(data) < offset+18 {
			return nil
		}
		ip := net.IP(data[offset : offset+16])
		port = uint16(data[offset+16])<<8 | uint16(data[offset+17])
		targetAddr = fmt.Sprintf("[%s]:%d", ip.String(), port)

	case protocol.AddrDomain:
		if len(data) < offset+1 {
			return nil
		}
		domainLen := int(data[offset])
		if len(data) < offset+1+domainLen+2 {
			return nil
		}
		domain := string(data[offset+1 : offset+1+domainLen])
		port = uint16(data[offset+1+domainLen])<<8 | uint16(data[offset+1+domainLen+1])
		targetAddr = fmt.Sprintf("%s:%d", domain, port)

	default:
		return h.buildConnectResponse(reqID, protocol.StatusError)
	}

	// 建立连接
	var conn net.Conn
	var dialErr error

	switch network {
	case protocol.NetworkTCP:
		conn, dialErr = net.DialTimeout("tcp", targetAddr, 10*time.Second)
	case protocol.NetworkUDP:
		conn, dialErr = net.DialTimeout("udp", targetAddr, 10*time.Second)
	default:
		return h.buildConnectResponse(reqID, protocol.StatusError)
	}

	if dialErr != nil {
		h.logDebug("连接失败 %s: %v", targetAddr, dialErr)
		return h.buildConnectResponse(reqID, protocol.StatusConnectFailed)
	}

	c := &Conn{
		ID:         reqID,
		Target:     conn,
		ClientAddr: from,
		LastActive: time.Now(),
		Network:    network,
	}
	h.conns.Store(reqID, c)

	// 启动读取协程
	go h.readFromTarget(c)

	h.logDebug("连接建立: %d -> %s", reqID, targetAddr)
	return h.buildConnectResponse(reqID, protocol.StatusOK)
}

func (h *Handler) handleData(data []byte, from *net.UDPAddr) []byte {
	if len(data) < 5 {
		return nil
	}

	connID := uint32(data[1])<<24 | uint32(data[2])<<16 | uint32(data[3])<<8 | uint32(data[4])
	payload := data[5:]

	v, ok := h.conns.Load(connID)
	if !ok {
		return nil
	}

	c := v.(*Conn)
	c.LastActive = time.Now()

	if c.Target != nil {
		_, err := c.Target.Write(payload)
		if err != nil {
			h.logDebug("写入目标失败: %v", err)
		}
	}

	return nil
}

func (h *Handler) handleDisconnect(data []byte, from *net.UDPAddr) []byte {
	if len(data) < 5 {
		return nil
	}

	connID := uint32(data[1])<<24 | uint32(data[2])<<16 | uint32(data[3])<<8 | uint32(data[4])

	if v, ok := h.conns.LoadAndDelete(connID); ok {
		c := v.(*Conn)
		if c.Target != nil {
			c.Target.Close()
		}
		h.logDebug("连接关闭: %d", connID)
	}

	return nil
}

func (h *Handler) buildConnectResponse(reqID uint32, status byte) []byte {
	resp := []byte{
		protocol.TypeConnectResp,
		byte(reqID >> 24),
		byte(reqID >> 16),
		byte(reqID >> 8),
		byte(reqID),
		status,
	}

	encrypted, err := h.crypto.Encrypt(resp)
	if err != nil {
		return nil
	}
	return encrypted
}

func (h *Handler) readFromTarget(c *Conn) {
	buf := make([]byte, 65535)
	for {
		if c.Target == nil {
			return
		}

		n, err := c.Target.Read(buf)
		if err != nil {
			h.conns.Delete(c.ID)
			return
		}

		c.LastActive = time.Now()

		// 构建数据包
		packet := make([]byte, 5+n)
		packet[0] = protocol.TypeData
		packet[1] = byte(c.ID >> 24)
		packet[2] = byte(c.ID >> 16)
		packet[3] = byte(c.ID >> 8)
		packet[4] = byte(c.ID)
		copy(packet[5:], buf[:n])

		encrypted, err := h.crypto.Encrypt(packet)
		if err != nil {
			continue
		}

		h.mu.Lock()
		sender := h.sender
		h.mu.Unlock()

		if sender != nil {
			sender(encrypted, c.ClientAddr)
		}
	}
}

func (h *Handler) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		h.cleanup()
	}
}

func (h *Handler) cleanup() {
	now := time.Now()
	h.conns.Range(func(key, value interface{}) bool {
		c := value.(*Conn)
		if now.Sub(c.LastActive) > 5*time.Minute {
			if c.Target != nil {
				c.Target.Close()
			}
			h.conns.Delete(key)
			h.logDebug("清理超时连接: %d", c.ID)
		}
		return true
	})
}

func (h *Handler) logDebug(format string, args ...interface{}) {
	if h.logLevel == "debug" {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// Close 关闭所有连接
func (h *Handler) Close() {
	h.conns.Range(func(key, value interface{}) bool {
		c := value.(*Conn)
		if c.Target != nil {
			c.Target.Close()
		}
		h.conns.Delete(key)
		return true
	})
}
