// internal/arq/arq_test.go
package arq

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestBasicSendRecv(t *testing.T) {
	var local, peer *Conn

	local = New(func(data []byte) error {
		if peer != nil {
			return peer.OnReceive(data)
		}
		return nil
	})

	peer = New(func(data []byte) error {
		if local != nil {
			return local.OnReceive(data)
		}
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
	data, err := peer.RecvTimeout(2 * time.Second)
	if err != nil {
		t.Fatalf("接收失败: %v", err)
	}

	if !bytes.Equal(data, testData) {
		t.Fatalf("数据不匹配: got %s, want %s", data, testData)
	}
}

func TestMultipleSend(t *testing.T) {
	var local, peer *Conn

	local = New(func(data []byte) error {
		if peer != nil {
			return peer.OnReceive(data)
		}
		return nil
	})

	peer = New(func(data []byte) error {
		if local != nil {
			return local.OnReceive(data)
		}
		return nil
	})

	defer local.Close()
	defer peer.Close()

	messages := []string{"msg1", "msg2", "msg3"}

	for _, msg := range messages {
		if err := local.Send([]byte(msg)); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}

	for i, expected := range messages {
		data, err := peer.RecvTimeout(2 * time.Second)
		if err != nil {
			t.Fatalf("接收消息 %d 失败: %v", i, err)
		}
		if string(data) != expected {
			t.Errorf("消息 %d 不匹配: got %s, want %s", i, data, expected)
		}
	}
}

func TestLargeData(t *testing.T) {
	var local, peer *Conn

	local = New(func(data []byte) error {
		if peer != nil {
			return peer.OnReceive(data)
		}
		return nil
	})

	peer = New(func(data []byte) error {
		if local != nil {
			return local.OnReceive(data)
		}
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
			data, err := peer.RecvTimeout(500 * time.Millisecond)
			if err == ErrTimeout {
				continue
			}
			if err != nil {
				t.Fatalf("接收失败: %v", err)
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

	small := make([]byte, 100)
	chunks := c.split(small)
	if len(chunks) != 1 {
		t.Errorf("小数据分片数错误: %d", len(chunks))
	}

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

	if err := local.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if err := local.Close(); err != nil {
		t.Fatalf("重复关闭失败: %v", err)
	}

	if err := local.Send([]byte("test")); err != ErrClosed {
		t.Errorf("关闭后发送应返回 ErrClosed, got: %v", err)
	}
}

func TestRecvTimeout(t *testing.T) {
	local := New(func(data []byte) error {
		return nil
	})
	defer local.Close()

	start := time.Now()
	_, err := local.RecvTimeout(100 * time.Millisecond)
	elapsed := time.Since(start)

	if err != ErrTimeout {
		t.Errorf("应该返回 ErrTimeout, got: %v", err)
	}

	if elapsed < 100*time.Millisecond {
		t.Errorf("超时时间太短: %v", elapsed)
	}
}

func TestGetStats(t *testing.T) {
	var local, peer *Conn

	local = New(func(data []byte) error {
		if peer != nil {
			return peer.OnReceive(data)
		}
		return nil
	})

	peer = New(func(data []byte) error {
		if local != nil {
			return local.OnReceive(data)
		}
		return nil
	})

	defer local.Close()
	defer peer.Close()

	if err := local.Send([]byte("test")); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	_, err := peer.RecvTimeout(time.Second)
	if err != nil {
		t.Fatalf("接收失败: %v", err)
	}

	localStats := local.GetStats()
	peerStats := peer.GetStats()

	if localStats.PacketsSent == 0 {
		t.Error("本地发送计数应该大于0")
	}
	if peerStats.PacketsRecv == 0 {
		t.Error("对端接收计数应该大于0")
	}
}

func TestConstants(t *testing.T) {
	if HeaderSize != 11 {
		t.Errorf("HeaderSize 应该是 11, got %d", HeaderSize)
	}
	if FlagData != 0x00 {
		t.Error("FlagData 应该是 0x00")
	}
	if FlagAck != 0x01 {
		t.Error("FlagAck 应该是 0x01")
	}
	if FlagPing != 0x02 {
		t.Error("FlagPing 应该是 0x02")
	}
	if FlagPong != 0x03 {
		t.Error("FlagPong 应该是 0x03")
	}
	if FlagFin != 0x04 {
		t.Error("FlagFin 应该是 0x04")
	}
}

func BenchmarkSendRecv(b *testing.B) {
	var local, peer *Conn

	local = New(func(data []byte) error {
		if peer != nil {
			peer.OnReceive(data)
		}
		return nil
	})

	peer = New(func(data []byte) error {
		if local != nil {
			local.OnReceive(data)
		}
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
