package main

import (
	"context"
	"errors"
	"github.com/heybox/omnimcp/internal/agentops"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func value(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func main() {
	addr := value("OMNIMCP_AGENT_ADDR", "127.0.0.1:8091")
	host, _, err := net.SplitHostPort(addr)
	if err != nil || (host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback())) {
		log.Fatal("运营工作台仅支持监听本机回环地址")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	store, err := agentops.NewStore(value("OMNIMCP_AGENT_STATE", filepath.Join(value("OMNIMCP_DATA_DIR", "data"), "agentops", "state.json")))
	if err != nil {
		log.Fatal(err)
	}
	model := os.Getenv("OMNIMCP_AGENT_MODEL")
	chat, err := agentops.NewChatGenerator(value("OMNIMCP_AGENT_BASE_URL", os.Getenv("OPENAI_BASE_URL")), model, value("OMNIMCP_AGENT_API_KEY", os.Getenv("OPENAI_API_KEY")))
	if err != nil {
		log.Fatal(err)
	}
	var generator agentops.Generator
	if chat != nil {
		generator = chat
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	engine := agentops.NewEngine(ctx, store, generator, &agentops.MCPReader{URL: value("OMNIMCP_AGENT_MCP_URL", "http://127.0.0.1:8090/mcp"), Token: value("OMNIMCP_AGENT_MCP_TOKEN", os.Getenv("OMNIMCP_API_TOKEN")), ApprovalToken: os.Getenv("OMNIMCP_APPROVAL_TOKEN")}, model)
	go engine.Schedule(ctx)
	server := &http.Server{Addr: addr, Handler: (&agentops.App{Engine: engine}).Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		engine.Cancel()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		_ = engine.Wait(shutdown)
	}()
	log.Printf("Agent operations: http://%s", addr)
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = engine.Wait(shutdown)
}
