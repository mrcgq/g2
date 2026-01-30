package server

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// PacketHandler 数据包处理接口
type PacketHandler interface {
	HandlePacket(data []byte, from *net.UDPAddr) []byte
}

// Server UDP 服务器
type Server struct {
	addr     string
	handler  PacketHandler
	logLevel int

	conn   *net.UDPConn
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New 创建服务器
func New(addr string, h PacketHandler, logLevel string) *Server {
	level := 1
	switch logLevel {
	case "debug":
		level = 2
	case "error":
		level = 0
	}

	return &Server{
		addr:     addr,
		handler:  h,
		logLevel: level,
		stopCh:   make(chan struct{}),
	}
}

// Start 启动服务器
func (s *Server) Start(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", s.addr)
	if err != nil {
		return fmt.Errorf("解析地址: %w", err)
	}

	s.conn, err = net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("监听失败: %w", err)
	}

	// 优化缓冲区（忽略错误，非关键）
	_ = s.conn.SetReadBuffer(4 * 1024 * 1024)
	_ = s.conn.SetWriteBuffer(4 * 1024 * 1024)

	// 启动工作协程
	workers := 4
	for i := 0; i < workers; i++ {
		s.wg.Add(1)
		go s.worker(ctx)
	}

	return nil
}

func (s *Server) worker(ctx context.Context) {
	defer s.wg.Done()

	buf := make([]byte, 65535)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}

		_ = s.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			select {
			case <-s.stopCh:
				return
			default:
				continue
			}
		}

		if n == 0 {
			continue
		}

		// 复制数据
		data := make([]byte, n)
		copy(data, buf[:n])

		// 异步处理
		go func(d []byte, a *net.UDPAddr) {
			if resp := s.handler.HandlePacket(d, a); resp != nil {
				_, _ = s.conn.WriteToUDP(resp, a)
			}
		}(data, addr)
	}
}

// SendTo 发送数据到指定地址
func (s *Server) SendTo(data []byte, addr *net.UDPAddr) error {
	if s.conn == nil {
		return fmt.Errorf("连接未初始化")
	}
	_, err := s.conn.WriteToUDP(data, addr)
	return err
}

// Stop 停止服务器
func (s *Server) Stop() {
	close(s.stopCh)
	if s.conn != nil {
		s.conn.Close()
	}
	s.wg.Wait()
}
