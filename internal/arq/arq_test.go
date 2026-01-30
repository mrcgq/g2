// internal/arq/arq_test.go
package arq

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestBasicSendRecv(t *testing.T) {
	var mu sync.Mutex
	var received [][]byte

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
	
	_ = received
}

func TestLargeData(t *testing.T) {
	var peer *Conn

	local := New(func(data []byte) error {
		if peer != nil {
			go peer.OnReceive(data)
		}
		return nil
	})

	peer = New(func(data []byte) error {
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
	for len(result) < len(largeData) {
		data, err := peer.RecvTimeout(time.Second)
		if err != nil {
			t.Fatalf("接收失败: %v (已收到 %d 字节)", err, len(result))
		}
		result = append(result, data...)
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

func BenchmarkSendRecv(b *testing.B) {
	var peer *Conn

	local := New(func(data []byte) error {
		if peer != nil {
			peer.OnReceive(data)
		}
		return nil
	})

	peer = New(func(data []byte) error {
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
