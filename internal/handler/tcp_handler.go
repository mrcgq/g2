// internal/handler/tcp_handler.go
package handler

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anthropics/phantom-server/internal/crypto"
	"github.com/anthropics/phantom-server/internal/protocol"
	"github.com/anthropics/phantom-server/internal/transport"
)

// TCPHandler TCP 连接处理器
type TCPHandler struct {
	crypto   *crypto.Crypto
	logLevel int

	activeConns int64
}

// NewTCPHandler 创建 TCP 处理器
func NewTCPHandler(c *crypto.Crypto, logLevel string) *TCPHandler {
	level := 1
	switch logLevel {
	case "debug":
		level = 2
	case "error":
		level = 0
	}

	return &TCPHandler{
		crypto:   c,
		logLevel: level,
	}
}

// HandleConnection 处理一个客户端连接
func (h *TCPHandler) HandleConnection(ctx context.Context, clientConn net.Conn) {
	atomic.AddInt64(&h.activeConns, 1)
	defer atomic.AddInt64(&h.activeConns, -1)

	clientAddr := clientConn.RemoteAddr().String()
	h.log(2, "新连接: %s", clientAddr)
	defer h.log(2, "连接关闭: %s", clientAddr)

	reader := transport.NewFrameReader(clientConn, transport.ReadTimeout)
	writer := transport.NewFrameWriter(clientConn, transport.WriteTimeout)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		encryptedFrame, err := reader.ReadFrame()
		if err != nil {
			if err != io.EOF {
				h.log(2, "读取帧失败: %s - %v", clientAddr, err)
			}
			return
		}

		plaintext, err := h.crypto.Decrypt(encryptedFrame)
		if err != nil {
			h.log(2, "解密失败: %s - %v", clientAddr, err)
			return
		}

		req, err := protocol.ParseRequest(plaintext)
		if err != nil {
			h.log(2, "解析请求失败: %s - %v", clientAddr, err)
			continue
		}

		switch req.Type {
		case protocol.TypeConnect:
			h.handleConnect(ctx, req, clientConn, reader, writer)
			return

		case protocol.TypeData:
			h.log(2, "收到孤立的 Data 请求: %s", clientAddr)
			continue

		case protocol.TypeClose:
			h.log(2, "收到 Close 请求: %s", clientAddr)
			return
		}
	}
}

// handleConnect 处理 Connect 请求
func (h *TCPHandler) handleConnect(
	ctx context.Context,
	req *protocol.Request,
	clientConn net.Conn,
	reader *transport.FrameReader,
	writer *transport.FrameWriter,
) {
	network := req.NetworkString()
	target := req.TargetAddr()

	h.log(1, "连接: %s %s (ID:%d)", network, target, req.ReqID)

	targetConn, err := net.DialTimeout(network, target, 10*time.Second)
	if err != nil {
		h.log(2, "连接目标失败: %s - %v", target, err)
		h.sendResponse(writer, req.ReqID, 0x01, nil)
		return
	}
	defer targetConn.Close()

	if tcpConn, ok := targetConn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	}

	if len(req.Data) > 0 {
		_ = targetConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := targetConn.Write(req.Data); err != nil {
			h.log(2, "发送初始数据失败: %v", err)
			h.sendResponse(writer, req.ReqID, 0x01, nil)
			return
		}
	}

	if err := h.sendResponse(writer, req.ReqID, 0x00, nil); err != nil {
		h.log(2, "发送响应失败: %v", err)
		return
	}

	h.log(1, "已建立: %s %s", network, target)

	h.proxy(ctx, req.ReqID, clientConn, targetConn, reader, writer)
}

// proxy 双向代理
func (h *TCPHandler) proxy(
	ctx context.Context,
	reqID uint32,
	clientConn net.Conn,
	targetConn net.Conn,
	reader *transport.FrameReader,
	writer *transport.FrameWriter,
) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		h.clientToTarget(ctx, reqID, clientConn, targetConn, reader)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		h.targetToClient(ctx, reqID, targetConn, writer)
	}()

	wg.Wait()
	h.log(1, "代理结束: ID:%d", reqID)
}

// clientToTarget 客户端到目标的数据转发
func (h *TCPHandler) clientToTarget(
	ctx context.Context,
	reqID uint32,
	clientConn net.Conn,
	targetConn net.Conn,
	reader *transport.FrameReader,
) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		encryptedFrame, err := reader.ReadFrame()
		if err != nil {
			if err != io.EOF {
				h.log(2, "读取客户端数据失败: ID:%d - %v", reqID, err)
			}
			return
		}

		plaintext, err := h.crypto.Decrypt(encryptedFrame)
		if err != nil {
			h.log(2, "解密失败: ID:%d - %v", reqID, err)
			return
		}

		req, err := protocol.ParseRequest(plaintext)
		if err != nil {
			h.log(2, "解析失败: ID:%d - %v", reqID, err)
			continue
		}

		switch req.Type {
		case protocol.TypeData:
			if len(req.Data) > 0 {
				_ = targetConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				if _, err := targetConn.Write(req.Data); err != nil {
					h.log(2, "写入目标失败: ID:%d - %v", reqID, err)
					return
				}
			}

		case protocol.TypeClose:
			h.log(2, "客户端主动关闭: ID:%d", reqID)
			return

		default:
			h.log(2, "意外的消息类型: %d", req.Type)
		}
	}
}

// targetToClient 目标到客户端的数据转发
func (h *TCPHandler) targetToClient(
	ctx context.Context,
	reqID uint32,
	targetConn net.Conn,
	writer *transport.FrameWriter,
) {
	buf := make([]byte, 32*1024)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_ = targetConn.SetReadDeadline(time.Now().Add(transport.ReadTimeout))
		n, err := targetConn.Read(buf)
		if err != nil {
			if err != io.EOF {
				h.log(2, "读取目标失败: ID:%d - %v", reqID, err)
			}
			_ = h.sendResponse(writer, reqID, protocol.TypeClose, nil)
			return
		}

		if err := h.sendResponse(writer, reqID, protocol.TypeData, buf[:n]); err != nil {
			h.log(2, "发送到客户端失败: ID:%d - %v", reqID, err)
			return
		}
	}
}

// sendResponse 发送响应
func (h *TCPHandler) sendResponse(writer *transport.FrameWriter, reqID uint32, status byte, data []byte) error {
	resp := protocol.BuildResponse(reqID, status, data)

	encrypted, err := h.crypto.Encrypt(resp)
	if err != nil {
		return fmt.Errorf("加密失败: %w", err)
	}

	return writer.WriteFrame(encrypted)
}

func (h *TCPHandler) log(level int, format string, args ...interface{}) {
	if level > h.logLevel {
		return
	}
	prefix := map[int]string{0: "[ERROR]", 1: "[INFO]", 2: "[DEBUG]"}[level]
	fmt.Printf("%s %s %s\n", prefix, time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// GetActiveConns 获取活跃连接数
func (h *TCPHandler) GetActiveConns() int64 {
	return atomic.LoadInt64(&h.activeConns)
}
