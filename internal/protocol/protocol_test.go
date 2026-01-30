package protocol

import (
	"encoding/binary"
	"testing"
)

func TestParseConnectIPv4(t *testing.T) {
	// Type(1) + ReqID(4) + Network(1) + AddrType(1) + IP(4) + Port(2)
	data := make([]byte, 13)
	data[0] = TypeConnect
	binary.BigEndian.PutUint32(data[1:5], 12345)
	data[5] = NetworkTCP
	data[6] = AddrIPv4
	copy(data[7:11], []byte{8, 8, 8, 8})
	binary.BigEndian.PutUint16(data[11:13], 443)

	req, err := ParseRequest(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if req.Type != TypeConnect {
		t.Errorf("Type 错误: %d", req.Type)
	}
	if req.ReqID != 12345 {
		t.Errorf("ReqID 错误: %d", req.ReqID)
	}
	if req.Address != "8.8.8.8" {
		t.Errorf("Address 错误: %s", req.Address)
	}
	if req.Port != 443 {
		t.Errorf("Port 错误: %d", req.Port)
	}
	if req.TargetAddr() != "8.8.8.8:443" {
		t.Errorf("TargetAddr 错误: %s", req.TargetAddr())
	}
}

func TestParseConnectDomain(t *testing.T) {
	domain := "example.com"
	// Type(1) + ReqID(4) + Network(1) + AddrType(1) + Len(1) + Domain + Port(2)
	data := make([]byte, 9+len(domain)+2)
	data[0] = TypeConnect
	binary.BigEndian.PutUint32(data[1:5], 99999)
	data[5] = NetworkTCP
	data[6] = AddrDomain
	data[7] = byte(len(domain))
	copy(data[8:8+len(domain)], domain)
	binary.BigEndian.PutUint16(data[8+len(domain):], 80)

	req, err := ParseRequest(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if req.Address != domain {
		t.Errorf("Address 错误: %s", req.Address)
	}
	if req.Port != 80 {
		t.Errorf("Port 错误: %d", req.Port)
	}
}

func TestParseData(t *testing.T) {
	payload := []byte("hello world")
	data := make([]byte, 5+len(payload))
	data[0] = TypeData
	binary.BigEndian.PutUint32(data[1:5], 54321)
	copy(data[5:], payload)

	req, err := ParseRequest(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if req.Type != TypeData {
		t.Errorf("Type 错误: %d", req.Type)
	}
	if string(req.Data) != string(payload) {
		t.Errorf("Data 错误: %s", req.Data)
	}
}

func TestBuildResponse(t *testing.T) {
	data := []byte("response")
	resp := BuildResponse(12345, 0x00, data)

	if resp[0] != TypeData {
		t.Errorf("Type 错误: %d", resp[0])
	}
	if binary.BigEndian.Uint32(resp[1:5]) != 12345 {
		t.Error("ReqID 错误")
	}
	if resp[5] != 0x00 {
		t.Errorf("Status 错误: %d", resp[5])
	}
	if string(resp[6:]) != string(data) {
		t.Errorf("Data 错误: %s", resp[6:])
	}
}
