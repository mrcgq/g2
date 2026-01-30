package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/anthropics/phantom-server/internal/crypto"
	"github.com/anthropics/phantom-server/internal/handler"
	"github.com/anthropics/phantom-server/internal/server"
	"gopkg.in/yaml.v3"
)

var (
	Version   = "3.0.0"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

type Config struct {
	Listen     string `yaml:"listen"`
	PSK        string `yaml:"psk"`
	TimeWindow int    `yaml:"time_window"`
	LogLevel   string `yaml:"log_level"`
}

func main() {
	configPath := flag.String("c", "config.yaml", "配置文件路径")
	showVersion := flag.Bool("v", false, "显示版本")
	genPSK := flag.Bool("gen-psk", false, "生成新的 PSK")
	flag.Parse()

	if *showVersion {
		fmt.Printf("Phantom Server v%s\n", Version)
		fmt.Printf("  Build: %s\n", BuildTime)
		fmt.Printf("  Commit: %s\n", GitCommit)
		return
	}

	if *genPSK {
		psk, _ := crypto.GeneratePSK()
		fmt.Println(psk)
		return
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		os.Exit(1)
	}

	cry, err := crypto.New(cfg.PSK, cfg.TimeWindow)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加密模块错误: %v\n", err)
		os.Exit(1)
	}

	h := handler.New(cry, cfg.LogLevel)
	srv := server.New(cfg.Listen, h, cfg.LogLevel)
	h.SetSender(srv.SendTo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}

	printBanner(cfg)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\n正在关闭...")
	cancel()
	srv.Stop()
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取失败: %w", err)
	}

	cfg := &Config{
		Listen:     ":54321",
		TimeWindow: 30,
		LogLevel:   "info",
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析失败: %w", err)
	}

	if cfg.PSK == "" {
		return nil, fmt.Errorf("psk 不能为空")
	}
	if cfg.TimeWindow < 1 || cfg.TimeWindow > 300 {
		return nil, fmt.Errorf("time_window 需在 1-300 之间")
	}

	return cfg, nil
}

func printBanner(cfg *Config) {
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║            Phantom Server v3.0                           ║")
	fmt.Println("║            极简 · 无状态 · 抗探测                        ║")
	fmt.Println("╠══════════════════════════════════════════════════════════╣")
	fmt.Printf("║  监听: %-49s ║\n", cfg.Listen+" (UDP)")
	fmt.Printf("║  时间窗口: %-45s ║\n", fmt.Sprintf("%d 秒", cfg.TimeWindow))
	fmt.Printf("║  日志级别: %-45s ║\n", cfg.LogLevel)
	fmt.Println("╠══════════════════════════════════════════════════════════╣")
	fmt.Println("║  按 Ctrl+C 停止                                          ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")
	fmt.Println()
}
