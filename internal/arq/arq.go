// internal/arq/arq.go
package arq

import (
	"encoding/binary"
	"errors"
	"sync"
	"time"
)

// ARQ 协议常量
const (
	HeaderSize  = 11 // Seq(4) + Ack(4) + Flags(1) + Len(2)
	MaxSegment  = 1200
	MaxInFlight = 256
	MaxRetries  = 10
	InitialRTO  = 500 * time.Millisecond
	MinRTO      = 100 * time.Millisecond
	MaxRTO      = 5 * time.Second
)

// 标志位
const (
	FlagData = 0x00
	FlagAck  = 0x01
	FlagPing = 0x02
	FlagPong = 0x03
	FlagFin  = 0x04
)

// 错误定义
var (
	ErrClosed      = errors.New("connection closed")
	ErrTimeout     = errors.New("connection timeout")
	ErrBufferFull  = errors.New("send buffer full")
	ErrInvalidData = errors.New("invalid data")
)

// Packet 待发送/重传的包
type Packet struct {
	Seq     uint32
	Data    []byte
	SentAt  time.Time
	Retries int
}

// Conn ARQ 连接
type Conn struct {
	// 发送侧
	sendSeq  uint32
	sendBuf  map[uint32]*Packet
	sendMu   sync.Mutex
	sendCond *sync.Cond

	// 接收侧
	recvSeq    uint32
	recvBuf    map[uint32][]byte
	recvMu     sync.Mutex
	recvReady  chan []byte
	lastRecvAt time.Time

	// RTT 估算
	srtt   time.Duration
	rttvar time.Duration
	rto    time.Duration
	rttMu  sync.RWMutex

	// 底层发送函数
	rawSend func([]byte) error

	// 控制
	closed   bool
	closeMu  sync.RWMutex
	closeCh  chan struct{}
	closeErr error

	// 统计
	stats Stats
}

// Stats 统计信息
type Stats struct {
	PacketsSent     uint64
	PacketsRecv     uint64
	PacketsRetrans  uint64
	PacketsDropped  uint64
	BytesSent       uint64
	BytesRecv       uint64
}

// New 创建 ARQ 连接
func New(sender func([]byte) error) *Conn {
	c := &Conn{
		sendSeq:    1,
		sendBuf:    make(map[uint32]*Packet),
		recvSeq:    1,
		recvBuf:    make(map[uint32][]byte),
		recvReady:  make(chan []byte, 64),
		rto:        InitialRTO,
		rawSend:    sender,
		closeCh:    make(chan struct{}),
		lastRecvAt: time.Now(),
	}
	c.sendCond = sync.NewCond(&c.sendMu)

	// 启动后台协程
	go c.retransmitLoop()
	go c.keepaliveLoop()

	return c
}

// Send 发送数据（自动分片）
func (c *Conn) Send(data []byte) error {
	if c.isClosed() {
		return ErrClosed
	}

	// 分片
	chunks := c.split(data)

	for _, chunk := range chunks {
		if err := c.sendChunk(chunk); err != nil {
			return err
		}
	}

	return nil
}

// sendChunk 发送单个分片
func (c *Conn) sendChunk(data []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	// 流控：等待发送窗口有空间
	for len(c.sendBuf) >= MaxInFlight {
		if c.isClosed() {
			return ErrClosed
		}
		c.sendCond.Wait()
	}

	seq := c.sendSeq
	c.sendSeq++

	pkt := &Packet{
		Seq:    seq,
		Data:   data,
		SentAt: time.Now(),
	}
	c.sendBuf[seq] = pkt

	// 发送
	return c.sendPacket(pkt)
}

// sendPacket 发送数据包
func (c *Conn) sendPacket(pkt *Packet) error {
	c.rttMu.RLock()
	ack := c.recvSeq - 1
	c.rttMu.RUnlock()

	frame := c.buildFrame(pkt.Seq, ack, FlagData, pkt.Data)
	
	c.stats.PacketsSent++
	c.stats.BytesSent += uint64(len(frame))
	
	return c.rawSend(frame)
}

// OnReceive 处理收到的数据
func (c *Conn) OnReceive(data []byte) error {
	if len(data) < HeaderSize {
		return ErrInvalidData
	}

	seq := binary.BigEndian.Uint32(data[0:4])
	ack := binary.BigEndian.Uint32(data[4:8])
	flags := data[8]
	length := binary.BigEndian.Uint16(data[9:11])

	if int(length) != len(data)-HeaderSize {
		return ErrInvalidData
	}

	payload := data[HeaderSize:]

	c.stats.PacketsRecv++
	c.stats.BytesRecv += uint64(len(data))

	// 更新最后接收时间
	c.recvMu.Lock()
	c.lastRecvAt = time.Now()
	c.recvMu.Unlock()

	// 处理 ACK（清理发送缓冲区）
	c.handleAck(ack)

	// 根据类型处理
	switch flags {
	case FlagData:
		return c.handleData(seq, payload)
	case FlagAck:
		return nil // 纯 ACK，已处理
	case FlagPing:
		return c.handlePing()
	case FlagPong:
		return nil // Pong 响应
	case FlagFin:
		return c.handleFin()
	default:
		return nil
	}
}

// handleData 处理数据包
func (c *Conn) handleData(seq uint32, payload []byte) error {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()

	// 发送 ACK
	defer c.sendAck()

	// 重复包
	if seq < c.recvSeq {
		return nil
	}

	// 正好是期望的包
	if seq == c.recvSeq {
		// 直接交付
		c.deliverData(payload)
		c.recvSeq++

		// 检查缓冲区中的后续包
		c.deliverBuffered()
		return nil
	}

	// 乱序包，缓存
	if _, exists := c.recvBuf[seq]; !exists {
		c.recvBuf[seq] = make([]byte, len(payload))
		copy(c.recvBuf[seq], payload)
	}

	return nil
}

// deliverData 交付数据到上层
func (c *Conn) deliverData(data []byte) {
	if len(data) == 0 {
		return
	}
	
	select {
	case c.recvReady <- data:
	default:
		// 缓冲区满，丢弃
		c.stats.PacketsDropped++
	}
}

// deliverBuffered 交付缓冲区中连续的包
func (c *Conn) deliverBuffered() {
	for {
		data, ok := c.recvBuf[c.recvSeq]
		if !ok {
			break
		}
		delete(c.recvBuf, c.recvSeq)
		c.deliverData(data)
		c.recvSeq++
	}
}

// handleAck 处理 ACK
func (c *Conn) handleAck(ack uint32) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	acked := false
	for seq, pkt := range c.sendBuf {
		if seq <= ack {
			// 计算 RTT（只对首次发送的包计算）
			if pkt.Retries == 0 {
				rtt := time.Since(pkt.SentAt)
				c.updateRTT(rtt)
			}
			delete(c.sendBuf, seq)
			acked = true
		}
	}

	if acked {
		c.sendCond.Broadcast()
	}
}

// sendAck 发送 ACK
func (c *Conn) sendAck() {
	ack := c.recvSeq - 1
	frame := c.buildFrame(0, ack, FlagAck, nil)
	_ = c.rawSend(frame)
}

// handlePing 处理心跳请求
func (c *Conn) handlePing() error {
	frame := c.buildFrame(0, 0, FlagPong, nil)
	return c.rawSend(frame)
}

// handleFin 处理关闭请求
func (c *Conn) handleFin() error {
	c.Close()
	return nil
}

// buildFrame 构建帧
func (c *Conn) buildFrame(seq, ack uint32, flags byte, payload []byte) []byte {
	frame := make([]byte, HeaderSize+len(payload))
	binary.BigEndian.PutUint32(frame[0:4], seq)
	binary.BigEndian.PutUint32(frame[4:8], ack)
	frame[8] = flags
	binary.BigEndian.PutUint16(frame[9:11], uint16(len(payload)))
	if len(payload) > 0 {
		copy(frame[HeaderSize:], payload)
	}
	return frame
}

// retransmitLoop 重传循环
func (c *Conn) retransmitLoop() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-c.closeCh:
			return
		case <-ticker.C:
			c.checkRetransmit()
		}
	}
}

// checkRetransmit 检查需要重传的包
func (c *Conn) checkRetransmit() {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if c.isClosed() {
		return
	}

	now := time.Now()
	rto := c.getRTO()

	for seq, pkt := range c.sendBuf {
		if now.Sub(pkt.SentAt) > rto {
			if pkt.Retries >= MaxRetries {
				// 超过最大重传次数
				c.closeWithError(ErrTimeout)
				return
			}

			pkt.Retries++
			pkt.SentAt = now
			c.stats.PacketsRetrans++

			// 重传
			_ = c.sendPacket(pkt)

			// 指数退避：增加 RTO
			c.rttMu.Lock()
			c.rto = min(c.rto*2, MaxRTO)
			c.rttMu.Unlock()

			// 每次只重传一个，避免突发
			_ = seq
			break
		}
	}
}

// keepaliveLoop 心跳循环
func (c *Conn) keepaliveLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.closeCh:
			return
		case <-ticker.C:
			if c.isClosed() {
				return
			}

			// 检查超时
			c.recvMu.Lock()
			idle := time.Since(c.lastRecvAt)
			c.recvMu.Unlock()

			if idle > 2*time.Minute {
				c.closeWithError(ErrTimeout)
				return
			}

			// 发送心跳
			frame := c.buildFrame(0, 0, FlagPing, nil)
			_ = c.rawSend(frame)
		}
	}
}

// updateRTT 更新 RTT 估算
func (c *Conn) updateRTT(sample time.Duration) {
	c.rttMu.Lock()
	defer c.rttMu.Unlock()

	if c.srtt == 0 {
		// 首次测量
		c.srtt = sample
		c.rttvar = sample / 2
	} else {
		// RFC 6298 算法
		diff := c.srtt - sample
		if diff < 0 {
			diff = -diff
		}
		c.rttvar = (3*c.rttvar + diff) / 4
		c.srtt = (7*c.srtt + sample) / 8
	}

	// RTO = SRTT + 4 * RTTVAR
	c.rto = c.srtt + 4*c.rttvar
	if c.rto < MinRTO {
		c.rto = MinRTO
	}
	if c.rto > MaxRTO {
		c.rto = MaxRTO
	}
}

// getRTO 获取当前 RTO
func (c *Conn) getRTO() time.Duration {
	c.rttMu.RLock()
	defer c.rttMu.RUnlock()
	return c.rto
}

// split 分片
func (c *Conn) split(data []byte) [][]byte {
	if len(data) <= MaxSegment {
		return [][]byte{data}
	}

	var chunks [][]byte
	for len(data) > 0 {
		size := MaxSegment
		if size > len(data) {
			size = len(data)
		}
		chunk := make([]byte, size)
		copy(chunk, data[:size])
		chunks = append(chunks, chunk)
		data = data[size:]
	}
	return chunks
}

// Recv 接收数据（阻塞）
func (c *Conn) Recv() ([]byte, error) {
	select {
	case data := <-c.recvReady:
		return data, nil
	case <-c.closeCh:
		return nil, c.closeErr
	}
}

// RecvTimeout 接收数据（带超时）
func (c *Conn) RecvTimeout(timeout time.Duration) ([]byte, error) {
	select {
	case data := <-c.recvReady:
		return data, nil
	case <-c.closeCh:
		return nil, c.closeErr
	case <-time.After(timeout):
		return nil, ErrTimeout
	}
}

// Close 关闭连接
func (c *Conn) Close() error {
	return c.closeWithError(nil)
}

// closeWithError 带错误关闭
func (c *Conn) closeWithError(err error) error {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return nil
	}
	c.closed = true
	c.closeErr = err
	close(c.closeCh)
	c.closeMu.Unlock()

	// 发送 FIN
	frame := c.buildFrame(0, 0, FlagFin, nil)
	_ = c.rawSend(frame)

	// 唤醒等待的发送者
	c.sendCond.Broadcast()

	return nil
}

// isClosed 检查是否已关闭
func (c *Conn) isClosed() bool {
	c.closeMu.RLock()
	defer c.closeMu.RUnlock()
	return c.closed
}

// GetStats 获取统计信息
func (c *Conn) GetStats() Stats {
	return c.stats
}

// GetRTT 获取当前 RTT
func (c *Conn) GetRTT() time.Duration {
	c.rttMu.RLock()
	defer c.rttMu.RUnlock()
	return c.srtt
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
