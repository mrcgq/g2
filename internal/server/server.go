// internal/server/server.go
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

	// 保序处理：按客户端地址哈希分配到固定 worker
	workers    int
	workerChs  []chan *packetTask
	workerWg   sync.WaitGroup
}

type packetTask struct {
	data []byte
	addr *net.UDPAddr
}

const (
	logError = 0
	logInfo  = 1
	logDebug = 2
)

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
		workers:  8, // 8 个 worker，按地址哈希分配
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

	// 优化缓冲区
	_ = s.conn.SetReadBuffer(4 * 1024 * 1024)
	_ = s.conn.SetWriteBuffer(4 * 1024 * 1024)

	// 初始化 worker 通道
	s.workerChs = make([]chan *packetTask, s.workers)
	for i := 0; i < s.workers; i++ {
		s.workerChs[i] = make(chan *packetTask, 1024)
		s.workerWg.Add(1)
		go s.orderedWorker(i)
	}

	// 启动读取协程
	s.wg.Add(1)
	go s.readLoop(ctx)

	s.log(logInfo, "服务器已启动: %s (workers: %d)", s.addr, s.workers)
	return nil
}

// readLoop 读取循环 - 只负责读取和分发
func (s *Server) readLoop(ctx context.Context) {
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

		// 按客户端地址哈希分配到固定 worker，保证同一客户端的包顺序处理
		workerIdx := s.hashAddr(addr) % s.workers
		
		select {
		case s.workerChs[workerIdx] <- &packetTask{data: data, addr: addr}:
		default:
			s.log(logDebug, "worker %d 队列满，丢弃包", workerIdx)
		}
	}
}

// orderedWorker 保序处理 worker
func (s *Server) orderedWorker(idx int) {
	defer s.workerWg.Done()

	for task := range s.workerChs[idx] {
		if task == nil {
			continue
		}

		// 同步处理，保证顺序
		if resp := s.handler.HandlePacket(task.data, task.addr); resp != nil {
			_, _ = s.conn.WriteToUDP(resp, task.addr)
		}
	}
}

// hashAddr 计算地址哈希
func (s *Server) hashAddr(addr *net.UDPAddr) int {
	// 使用 IP + Port 组合哈希
	hash := 0
	for _, b := range addr.IP {
		hash = hash*31 + int(b)
	}
	hash = hash*31 + addr.Port
	if hash < 0 {
		hash = -hash
	}
	return hash
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
	
	// 关闭所有 worker 通道
	for _, ch := range s.workerChs {
		close(ch)
	}
	s.workerWg.Wait()
	
	if s.conn != nil {
		s.conn.Close()
	}
	s.wg.Wait()
}

func (s *Server) log(level int, format string, args ...interface{}) {
	if level > s.logLevel {
		return
	}
	prefix := map[int]string{logError: "[ERROR]", logInfo: "[INFO]", logDebug: "[DEBUG]"}[level]
	fmt.Printf("%s %s %s\n", prefix, time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
