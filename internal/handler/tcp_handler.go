//internal/handler/tcp_handler.go
package handler

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/anthropics/phantom-server/internal/crypto"
	"github.com/anthropics/phantom-server/internal/protocol"
	"github.com/anthropics/phantom-server/internal/transport"
)

// Conn 表示一个代理连接
type Conn struct {
	ID         uint32
	Target     net.Conn
	ClientConn net.Conn
	Writer     *transport.FrameWriter
	LastActive time.Time
	Network    byte
	closed     bool
	mu         sync.Mutex
}

// TCPHandler 处理 TCP 代理请求
type TCPHandler struct {
	crypto   *crypto.Crypto
	conns    sync.Map // map[uint32]*Conn
	logLevel string
}

// NewTCPHandler 创建新的 TCP Handler
func NewTCPHandler(c *crypto.Crypto, logLevel string) *TCPHandler {
	h := &TCPHandler{
		crypto:   c,
		logLevel: logLevel,
	}
	go h.cleanupLoop()
	return h
}

// HandleConnection 实现 PacketHandler 接口，处理单个客户端 TCP 连接
func (h *TCPHandler) HandleConnection(ctx context.Context, conn net.Conn) {
	reader := transport.NewFrameReader(conn, transport.ReadTimeout)
	writer := transport.NewFrameWriter(conn, transport.WriteTimeout)

	h.logDebug("处理新连接: %s", conn.RemoteAddr())

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 读取加密帧
		frame, err := reader.ReadFrame()
		if err != nil {
			if err != io.EOF {
				h.logDebug("读取帧失败 [%s]: %v", conn.RemoteAddr(), err)
			}
			return
		}

		h.logDebug("收到帧: %d 字节", len(frame))

		// 解密
		plaintext, err := h.crypto.Decrypt(frame)
		if err != nil {
			h.logDebug("解密失败: %v", err)
			// 静默丢弃无效数据，不断开连接
			continue
		}

		if len(plaintext) < 1 {
			continue
		}

		// 处理消息
		msgType := plaintext[0]
		h.logDebug("消息类型: 0x%02x", msgType)

		switch msgType {
		case protocol.TypeConnect:
			response := h.handleConnect(plaintext, conn, writer)
			if response != nil {
				if err := writer.WriteFrame(response); err != nil {
					h.logDebug("发送连接响应失败: %v", err)
					return
				}
			}
		case protocol.TypeData:
			h.handleData(plaintext)
		case protocol.TypeDisconnect:
			h.handleDisconnect(plaintext)
		default:
			h.logDebug("未知消息类型: 0x%02x", msgType)
		}
	}
}

func (h *TCPHandler) handleConnect(data []byte, clientConn net.Conn, writer *transport.FrameWriter) []byte {
	if len(data) < 7 {
		h.logDebug("Connect 数据太短: %d", len(data))
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
		h.logDebug("未知地址类型: 0x%02x", addrType)
		return h.buildConnectResponse(reqID, protocol.StatusError)
	}

	// 确定网络类型
	var networkStr string
	switch network {
	case protocol.NetworkTCP:
		networkStr = "tcp"
	case protocol.NetworkUDP:
		networkStr = "udp"
	default:
		h.logDebug("未知网络类型: 0x%02x", network)
		return h.buildConnectResponse(reqID, protocol.StatusError)
	}

	h.logDebug("连接请求: ID=%d, %s -> %s", reqID, networkStr, targetAddr)

	// 建立到目标的连接
	targetConn, err := net.DialTimeout(networkStr, targetAddr, 10*time.Second)
	if err != nil {
		h.logDebug("连接目标失败 %s: %v", targetAddr, err)
		return h.buildConnectResponse(reqID, protocol.StatusConnectFailed)
	}

	c := &Conn{
		ID:         reqID,
		Target:     targetConn,
		ClientConn: clientConn,
		Writer:     writer,
		LastActive: time.Now(),
		Network:    network,
	}
	h.conns.Store(reqID, c)

	// 启动从目标读取数据的协程
	go h.readFromTarget(c)

	h.logDebug("连接建立成功: ID=%d -> %s", reqID, targetAddr)
	return h.buildConnectResponse(reqID, protocol.StatusOK)
}

func (h *TCPHandler) handleData(data []byte) {
	if len(data) < 5 {
		return
	}

	connID := uint32(data[1])<<24 | uint32(data[2])<<16 | uint32(data[3])<<8 | uint32(data[4])
	payload := data[5:]

	v, ok := h.conns.Load(connID)
	if !ok {
		h.logDebug("连接不存在: %d", connID)
		return
	}

	c := v.(*Conn)
	c.mu.Lock()
	c.LastActive = time.Now()
	target := c.Target
	c.mu.Unlock()

	if target != nil {
		n, err := target.Write(payload)
		if err != nil {
			h.logDebug("写入目标失败: %v", err)
		} else {
			h.logDebug("发送到目标: %d 字节", n)
		}
	}
}

func (h *TCPHandler) handleDisconnect(data []byte) {
	if len(data) < 5 {
		return
	}

	connID := uint32(data[1])<<24 | uint32(data[2])<<16 | uint32(data[3])<<8 | uint32(data[4])

	if v, ok := h.conns.LoadAndDelete(connID); ok {
		c := v.(*Conn)
		c.mu.Lock()
		c.closed = true
		if c.Target != nil {
			c.Target.Close()
		}
		c.mu.Unlock()
		h.logDebug("连接关闭: %d", connID)
	}
}

func (h *TCPHandler) buildConnectResponse(reqID uint32, status byte) []byte {
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
		h.logDebug("加密响应失败: %v", err)
		return nil
	}
	return encrypted
}

func (h *TCPHandler) readFromTarget(c *Conn) {
	buf := make([]byte, 32*1024) // 32KB 缓冲

	defer func() {
		h.conns.Delete(c.ID)
		c.mu.Lock()
		if c.Target != nil {
			c.Target.Close()
		}
		c.mu.Unlock()
	}()

	for {
		c.mu.Lock()
		if c.closed || c.Target == nil {
			c.mu.Unlock()
			return
		}
		target := c.Target
		c.mu.Unlock()

		n, err := target.Read(buf)
		if err != nil {
			if err != io.EOF {
				h.logDebug("读取目标失败: %v", err)
			}
			return
		}

		c.mu.Lock()
		c.LastActive = time.Now()
		c.mu.Unlock()

		h.logDebug("从目标收到: %d 字节", n)

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
			h.logDebug("加密数据失败: %v", err)
			continue
		}

		// 发送加密数据到客户端
		if err := c.Writer.WriteFrame(encrypted); err != nil {
			h.logDebug("发送数据到客户端失败: %v", err)
			return
		}
	}
}

func (h *TCPHandler) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		h.cleanup()
	}
}

func (h *TCPHandler) cleanup() {
	now := time.Now()
	h.conns.Range(func(key, value interface{}) bool {
		c := value.(*Conn)
		c.mu.Lock()
		lastActive := c.LastActive
		c.mu.Unlock()

		if now.Sub(lastActive) > 5*time.Minute {
			c.mu.Lock()
			c.closed = true
			if c.Target != nil {
				c.Target.Close()
			}
			c.mu.Unlock()
			h.conns.Delete(key)
			h.logDebug("清理超时连接: %d", c.ID)
		}
		return true
	})
}

func (h *TCPHandler) logDebug(format string, args ...interface{}) {
	if h.logLevel == "debug" {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// Close 关闭所有连接
func (h *TCPHandler) Close() {
	h.conns.Range(func(key, value interface{}) bool {
		c := value.(*Conn)
		c.mu.Lock()
		c.closed = true
		if c.Target != nil {
			c.Target.Close()
		}
		c.mu.Unlock()
		h.conns.Delete(key)
		return true
	})
}
