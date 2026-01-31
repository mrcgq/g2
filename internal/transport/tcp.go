// internal/transport/tcp.go
package transport

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	LengthPrefixSize = 2
	MaxPacketSize    = 65535
	ReadTimeout      = 5 * time.Minute
	WriteTimeout     = 30 * time.Second
)

// TCPServer TCP 服务器
type TCPServer struct {
	addr     string
	listener net.Listener
	handler  PacketHandler
	logLevel int

	conns  sync.Map
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// PacketHandler 数据包处理接口
type PacketHandler interface {
	HandleConnection(ctx context.Context, conn net.Conn)
}

// NewTCPServer 创建 TCP 服务器
func NewTCPServer(addr string, handler PacketHandler, logLevel string) *TCPServer {
	level := 1
	switch logLevel {
	case "debug":
		level = 2
	case "error":
		level = 0
	}

	return &TCPServer{
		addr:     addr,
		handler:  handler,
		logLevel: level,
		stopCh:   make(chan struct{}),
	}
}

// Start 启动服务器
func (s *TCPServer) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("监听失败: %w", err)
	}
	s.listener = listener

	s.wg.Add(1)
	go s.acceptLoop(ctx)

	s.log(1, "TCP 服务器已启动: %s", s.addr)
	return nil
}

// acceptLoop 接受连接循环
func (s *TCPServer) acceptLoop(ctx context.Context) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}

		if tcpListener, ok := s.listener.(*net.TCPListener); ok {
			_ = tcpListener.SetDeadline(time.Now().Add(time.Second))
		}

		conn, err := s.listener.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			select {
			case <-s.stopCh:
				return
			default:
				s.log(2, "Accept 错误: %v", err)
				continue
			}
		}

		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.SetNoDelay(true)
			_ = tcpConn.SetKeepAlive(true)
			_ = tcpConn.SetKeepAlivePeriod(30 * time.Second)
		}

		s.conns.Store(conn, struct{}{})

		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer func() {
				s.conns.Delete(c)
				_ = c.Close()
			}()
			s.handler.HandleConnection(ctx, c)
		}(conn)
	}
}

// Stop 停止服务器
func (s *TCPServer) Stop() {
	close(s.stopCh)

	if s.listener != nil {
		_ = s.listener.Close()
	}

	s.conns.Range(func(key, _ interface{}) bool {
		if conn, ok := key.(net.Conn); ok {
			_ = conn.Close()
		}
		return true
	})

	s.wg.Wait()
}

func (s *TCPServer) log(level int, format string, args ...interface{}) {
	if level > s.logLevel {
		return
	}
	prefix := map[int]string{0: "[ERROR]", 1: "[INFO]", 2: "[DEBUG]"}[level]
	fmt.Printf("%s %s %s\n", prefix, time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// FrameReader 帧读取器
type FrameReader struct {
	conn    net.Conn
	buf     []byte
	timeout time.Duration
}

// NewFrameReader 创建帧读取器
func NewFrameReader(conn net.Conn, timeout time.Duration) *FrameReader {
	return &FrameReader{
		conn:    conn,
		buf:     make([]byte, MaxPacketSize+LengthPrefixSize),
		timeout: timeout,
	}
}

// ReadFrame 读取一个完整的帧
func (r *FrameReader) ReadFrame() ([]byte, error) {
	if r.timeout > 0 {
		_ = r.conn.SetReadDeadline(time.Now().Add(r.timeout))
	}

	lengthBuf := r.buf[:LengthPrefixSize]
	if _, err := io.ReadFull(r.conn, lengthBuf); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint16(lengthBuf)
	if length == 0 {
		return nil, fmt.Errorf("无效的帧长度: 0")
	}
	if length > MaxPacketSize {
		return nil, fmt.Errorf("帧太大: %d", length)
	}

	data := r.buf[LengthPrefixSize : LengthPrefixSize+length]
	if _, err := io.ReadFull(r.conn, data); err != nil {
		return nil, err
	}

	result := make([]byte, length)
	copy(result, data)
	return result, nil
}

// FrameWriter 帧写入器
type FrameWriter struct {
	conn    net.Conn
	buf     []byte
	timeout time.Duration
	mu      sync.Mutex
}

// NewFrameWriter 创建帧写入器
func NewFrameWriter(conn net.Conn, timeout time.Duration) *FrameWriter {
	return &FrameWriter{
		conn:    conn,
		buf:     make([]byte, MaxPacketSize+LengthPrefixSize),
		timeout: timeout,
	}
}

// WriteFrame 写入一个帧
func (w *FrameWriter) WriteFrame(data []byte) error {
	if len(data) > MaxPacketSize {
		return fmt.Errorf("数据太大: %d > %d", len(data), MaxPacketSize)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.timeout > 0 {
		_ = w.conn.SetWriteDeadline(time.Now().Add(w.timeout))
	}

	binary.BigEndian.PutUint16(w.buf[:LengthPrefixSize], uint16(len(data)))
	copy(w.buf[LengthPrefixSize:], data)

	total := LengthPrefixSize + len(data)
	_, err := w.conn.Write(w.buf[:total])
	return err
}
