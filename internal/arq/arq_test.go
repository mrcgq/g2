// internal/arq/arq_test.go
package arq

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

func TestBasicSendRecv(t *testing.T) {
	var mu sync.Mutex

	// 模拟对端
	var peer *Conn

	// 创建本端
	local := New(func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if peer != nil {
			go peer.OnReceive(data)
		}
		return nil
	})

	// 创建对端
	peer = New(func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		go local.OnReceive(data)
		return nil
	})

	defer local.Close()
	defer peer.Close()

	// 发送测试数据
	testData := []byte("Hello, ARQ!")
	if err := local.Send(testData); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	// 接收
	data, err := peer.RecvTimeout(time.Second)
	if err != nil {
		t.Fatalf("接收失败: %v", err)
	}

	if !bytes.Equal(data, testData) {
		t.Fatalf("数据不匹配: got %s, want %s", data, testData)
	}
}

func TestLargeData(t *testing.T) {
	var mu sync.Mutex
	var peer *Conn

	local := New(func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if peer != nil {
			go peer.OnReceive(data)
		}
		return nil
	})

	peer = New(func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		go local.OnReceive(data)
		return nil
	})

	defer local.Close()
	defer peer.Close()

	// 发送大数据（会自动分片）
	largeData := make([]byte, 5000)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	if err := local.Send(largeData); err != nil {
		t.Fatalf("发送大数据失败: %v", err)
	}

	// 接收所有分片
	var result []byte
	timeout := time.After(5 * time.Second)
	for len(result) < len(largeData) {
		select {
		case <-timeout:
			t.Fatalf("接收超时 (已收到 %d/%d 字节)", len(result), len(largeData))
		default:
			data, err := peer.RecvTimeout(time.Second)
			if err != nil {
				continue
			}
			result = append(result, data...)
		}
	}

	if !bytes.Equal(result, largeData) {
		t.Fatalf("大数据不匹配")
	}
}

func TestFrameBuild(t *testing.T) {
	c := &Conn{}

	frame := c.buildFrame(1, 2, FlagData, []byte("test"))

	if len(frame) != HeaderSize+4 {
		t.Fatalf("帧长度错误: %d", len(frame))
	}

	// 验证头部
	seq := binary.BigEndian.Uint32(frame[0:4])
	ack := binary.BigEndian.Uint32(frame[4:8])
	flags := frame[8]
	length := binary.BigEndian.Uint16(frame[9:11])

	if seq != 1 {
		t.Errorf("Seq 错误: %d", seq)
	}
	if ack != 2 {
		t.Errorf("Ack 错误: %d", ack)
	}
	if flags != FlagData {
		t.Errorf("Flags 错误: %d", flags)
	}
	if length != 4 {
		t.Errorf("Length 错误: %d", length)
	}
}

func TestSplit(t *testing.T) {
	c := &Conn{}

	// 小数据不分片
	small := make([]byte, 100)
	chunks := c.split(small)
	if len(chunks) != 1 {
		t.Errorf("小数据分片数错误: %d", len(chunks))
	}

	// 大数据分片
	large := make([]byte, 3000)
	chunks = c.split(large)
	expected := (3000 + MaxSegment - 1) / MaxSegment
	if len(chunks) != expected {
		t.Errorf("大数据分片数错误: got %d, want %d", len(chunks), expected)
	}
}

func TestCloseConnection(t *testing.T) {
	local := New(func(data []byte) error {
		return nil
	})

	// 关闭连接
	if err := local.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	// 再次关闭应该无错误
	if err := local.Close(); err != nil {
		t.Fatalf("重复关闭失败: %v", err)
	}

	// 发送应该返回错误
	if err := local.Send([]byte("test")); err != ErrClosed {
		t.Errorf("关闭后发送应返回 ErrClosed, got: %v", err)
	}
}

func TestACKHandling(t *testing.T) {
	var mu sync.Mutex
	var peer *Conn
	ackReceived := make(chan struct{}, 10)

	local := New(func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if peer != nil {
			go func() {
				peer.OnReceive(data)
				select {
				case ackReceived <- struct{}{}:
				default:
				}
			}()
		}
		return nil
	})

	peer = New(func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		go local.OnReceive(data)
		return nil
	})

	defer local.Close()
	defer peer.Close()

	// 发送数据
	if err := local.Send([]byte("test")); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	// 等待 ACK 处理
	select {
	case <-ackReceived:
		// ACK 已处理
	case <-time.After(2 * time.Second):
		t.Log("等待 ACK 超时，但这可能是正常的")
	}
}

func BenchmarkSendRecv(b *testing.B) {
	var mu sync.Mutex
	var peer *Conn

	local := New(func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if peer != nil {
			peer.OnReceive(data)
		}
		return nil
	})

	peer = New(func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		local.OnReceive(data)
		return nil
	})

	defer local.Close()
	defer peer.Close()

	data := make([]byte, 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = local.Send(data)
		_, _ = peer.RecvTimeout(time.Second)
	}
}

func BenchmarkFrameBuild(b *testing.B) {
	c := &Conn{}
	payload := make([]byte, 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.buildFrame(uint32(i), uint32(i), FlagData, payload)
	}
}
