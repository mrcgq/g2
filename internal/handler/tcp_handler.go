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

	// 活跃连接计数
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

	// 创建帧读写器
	reader := transport.NewFrameReader(clientConn, transport.ReadTimeout)
	writer := transport.NewFrameWriter(clientConn, transport.WriteTimeout)

	// 读取并处理请求
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 读取加密帧
		encryptedFrame, err := reader.ReadFrame()
		if err != nil {
			if err != io.EOF {
				h.log(2, "读取帧失败: %s - %v", clientAddr, err)
			}
			return
		}

		// 解密
		plaintext, err := h.crypto.Decrypt(encryptedFrame)
		if err != nil {
			h.log(2, "解密失败: %s - %v", clientAddr, err)
			return // 解密失败断开连接（可能是攻击）
		}

		// 解析请求
		req, err := protocol.ParseRequest(plaintext)
		if err != nil {
			h.log(2, "解析请求失败: %s - %v", clientAddr, err)
			continue
		}

		// 处理请求
		switch req.Type {
		case protocol.TypeConnect:
			// Connect 请求：建立到目标的连接并开始代理
			h.handleConnect(ctx, req, clientConn, reader, writer)
			return // Connect 完成后退出（代理循环在 handleConnect 中）

		case protocol.TypeData:
			// 孤立的 Data 请求（没有 Connect）
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

	// 连接目标服务器
	targetConn, err := net.DialTimeout(network, target, 10*time.Second)
	if err != nil {
		h.log(2, "连接目标失败: %s - %v", target, err)
		h.sendResponse(writer, req.ReqID, 0x01, nil) // 失败
		return
	}
	defer targetConn.Close()

	// 配置目标连接
	if tcpConn, ok := targetConn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	}

	// 如果有初始数据，发送到目标
	if len(req.Data) > 0 {
		_ = targetConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := targetConn.Write(req.Data); err != nil {
			h.log(2, "发送初始数据失败: %v", err)
			h.sendResponse(writer, req.ReqID, 0x01, nil)
			return
		}
	}

	// 发送成功响应
	if err := h.sendResponse(writer, req.ReqID, 0x00, nil); err != nil {
		h.log(2, "发送响应失败: %v", err)
		return
	}

	h.log(1, "已建立: %s %s", network, target)

	// 开始双向代理
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

	// 客户端 -> 目标
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		h.clientToTarget(ctx, reqID, clientConn, targetConn, reader)
	}()

	// 目标 -> 客户端
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

		// 读取加密帧
		encryptedFrame, err := reader.ReadFrame()
		if err != nil {
			if err != io.EOF {
				h.log(2, "读取客户端数据失败: ID:%d - %v", reqID, err)
			}
			return
		}

		// 解密
		plaintext, err := h.crypto.Decrypt(encryptedFrame)
		if err != nil {
			h.log(2, "解密失败: ID:%d - %v", reqID, err)
			return
		}

		// 解析
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

		// 从目标读取
		_ = targetConn.SetReadDeadline(time.Now().Add(transport.ReadTimeout))
		n, err := targetConn.Read(buf)
		if err != nil {
			if err != io.EOF {
				h.log(2, "读取目标失败: ID:%d - %v", reqID, err)
			}
			// 发送关闭通知
			_ = h.sendResponse(writer, reqID, protocol.TypeClose, nil)
			return
		}

		// 发送到客户端
		if err := h.sendResponse(writer, reqID, protocol.TypeData, buf[:n]); err != nil {
			h.log(2, "发送到客户端失败: ID:%d - %v", reqID, err)
			return
		}
	}
}

// sendResponse 发送响应
func (h *TCPHandler) sendResponse(writer *transport.FrameWriter, reqID uint32, status byte, data []byte) error {
	// 构建响应
	resp := protocol.BuildResponse(reqID, status, data)

	// 加密
	encrypted, err := h.crypto.Encrypt(resp)
	if err != nil {
		return fmt.Errorf("加密失败: %w", err)
	}

	// 发送帧
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
