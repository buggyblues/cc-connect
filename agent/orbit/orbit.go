package orbit

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("orbit", New)
}

// Agent connects cc-connect to Orbit's External Gateway socket.
type Agent struct {
	socketPath      string
	socketPathSet   bool
	workDir         string
	fallbackAgent   string
	autoReconnect   bool
	heartbeat       time.Duration
	throttleDelta   time.Duration
	delegateOptions map[string]any
	fallbackOptions map[string]any
	sessionEnv      []string
	platformPrompt  string
	mu              sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	if opts == nil {
		opts = map[string]any{}
	}
	workDir, _ := opts["work_dir"].(string)
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	workDir = expandHome(workDir)

	socketPath, _ := opts["socket_path"].(string)
	socketPathSet := strings.TrimSpace(socketPath) != ""
	if strings.TrimSpace(socketPath) == "" {
		socketPath = filepath.Join(workDir, ".orbit", "external-gateway.sock")
	}
	socketPath = expandHome(socketPath)

	fallbackAgent, _ := opts["fallback_agent"].(string)
	autoReconnect := boolOption(opts, "auto_reconnect", true)
	heartbeatSeconds := 30
	if _, ok := opts["heartbeat_seconds"]; ok {
		heartbeatSeconds = intOption(opts, "heartbeat_seconds", 30)
	}
	if heartbeatSeconds < 0 {
		heartbeatSeconds = 0
	}
	throttleMs := intOption(opts, "throttle_delta_ms", 500)
	if throttleMs < 0 {
		throttleMs = 0
	}

	return &Agent{
		socketPath:      socketPath,
		socketPathSet:   socketPathSet,
		workDir:         workDir,
		fallbackAgent:   strings.TrimSpace(fallbackAgent),
		autoReconnect:   autoReconnect,
		heartbeat:       time.Duration(heartbeatSeconds) * time.Second,
		throttleDelta:   time.Duration(throttleMs) * time.Millisecond,
		delegateOptions: mapOption(opts, "delegate_options"),
		fallbackOptions: mapOption(opts, "fallback_options"),
	}, nil
}

func (a *Agent) Name() string           { return "orbit" }
func (a *Agent) CLIBinaryName() string  { return "orbit" }
func (a *Agent) CLIDisplayName() string { return "Orbit" }

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.RLock()
	cfg := sessionConfig{
		socketPath:      a.socketPath,
		workDir:         a.workDir,
		fallbackAgent:   a.fallbackAgent,
		autoReconnect:   a.autoReconnect,
		heartbeat:       a.heartbeat,
		throttleDelta:   a.throttleDelta,
		delegateOptions: cloneMap(a.delegateOptions),
		fallbackOptions: cloneMap(a.fallbackOptions),
		sessionEnv:      append([]string{}, a.sessionEnv...),
		platformPrompt:  a.platformPrompt,
	}
	a.mu.RUnlock()
	return newSession(ctx, cfg, sessionID), nil
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (a *Agent) Stop() error { return nil }

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = append([]string{}, env...)
}

func (a *Agent) SetPlatformPrompt(prompt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.platformPrompt = prompt
}

func (a *Agent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(dir) == "" {
		return
	}
	a.workDir = expandHome(dir)
	if !a.socketPathSet {
		a.socketPath = filepath.Join(a.workDir, ".orbit", "external-gateway.sock")
	}
	slog.Info("orbit: work_dir changed", "work_dir", a.workDir)
}

func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	opts := map[string]any{
		"fallback_agent":    a.fallbackAgent,
		"auto_reconnect":    a.autoReconnect,
		"heartbeat_seconds": int(a.heartbeat / time.Second),
		"throttle_delta_ms": int(a.throttleDelta / time.Millisecond),
		"delegate_options":  cloneMap(a.delegateOptions),
		"fallback_options":  cloneMap(a.fallbackOptions),
	}
	if a.socketPathSet {
		opts["socket_path"] = a.socketPath
	}
	return opts
}

func boolOption(opts map[string]any, key string, def bool) bool {
	v, ok := opts[key]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		if parsed, err := strconv.ParseBool(strings.TrimSpace(x)); err == nil {
			return parsed
		}
	}
	return def
}

func intOption(opts map[string]any, key string, def int) int {
	v, ok := opts[key]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return parsed
		}
	}
	return def
}

func mapOption(opts map[string]any, key string) map[string]any {
	switch v := opts[key].(type) {
	case map[string]any:
		return cloneMap(v)
	default:
		return nil
	}
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func optionString(opts map[string]any, key string) string {
	if opts == nil {
		return ""
	}
	v, _ := opts[key].(string)
	return strings.TrimSpace(v)
}

func ensureWorkDirOption(opts map[string]any, workDir string) map[string]any {
	out := cloneMap(opts)
	if out == nil {
		out = make(map[string]any)
	}
	if strings.TrimSpace(workDir) != "" {
		out["work_dir"] = workDir
	}
	if optionString(out, "work_dir") == "" {
		out["work_dir"] = "."
	}
	return out
}

func createAgent(name string, opts map[string]any) (core.Agent, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("agent name is empty")
	}
	if strings.EqualFold(name, "orbit") {
		return nil, fmt.Errorf("orbit: refusing recursive delegate to orbit")
	}
	return core.CreateAgent(name, opts)
}
