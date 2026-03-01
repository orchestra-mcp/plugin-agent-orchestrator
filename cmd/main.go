package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/plugin"
	"github.com/orchestra-mcp/plugin-agent-orchestrator/internal"
	"github.com/orchestra-mcp/plugin-agent-orchestrator/internal/storage"
)

func main() {
	builder := plugin.New("agent.orchestrator").
		Version("0.1.0").
		Description("Multi-agent orchestration — define agents, workflows, and execute them across LLM providers").
		Author("Orchestra").
		Binary("agent-orchestrator").
		NeedsStorage("markdown")

	adapter := &clientAdapter{}
	store := storage.NewDataStorage(adapter)

	tp := &internal.ToolsPlugin{Storage: store}
	tp.RegisterTools(builder)

	p := builder.BuildWithTools()
	p.ParseFlags()
	adapter.plugin = p

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	if err := p.Run(ctx); err != nil {
		log.Fatalf("agent.orchestrator: %v", err)
	}
}

type clientAdapter struct {
	plugin *plugin.Plugin
}

func (a *clientAdapter) Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
	client := a.plugin.OrchestratorClient()
	if client == nil {
		return nil, fmt.Errorf("orchestrator client not connected")
	}
	return client.Send(ctx, req)
}
