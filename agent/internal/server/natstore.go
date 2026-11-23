package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

func natStorePath() string {
	if path := os.Getenv("DEVIY_NAT_PATH"); path != "" {
		return path
	}

	return "/var/lib/deviy/nat.json"
}

func (a *App) loadNAT() {
	raw, err := os.ReadFile(natStorePath())
	if err != nil {
		return
	}
	var rules []NATRule
	if json.Unmarshal(raw, &rules) != nil {
		return
	}
	a.mu.Lock()
	a.nat = rules
	a.mu.Unlock()
}

func (a *App) saveNAT() {
	a.mu.RLock()
	rules := append([]NATRule(nil), a.nat...)
	a.mu.RUnlock()
	raw, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return
	}
	path := natStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(temp, path)
}

func (a *App) applyPersistedNAT() {
	time.Sleep(time.Second)
	if !a.allowNetworkApply {
		return
	}
	a.mu.RLock()
	rules := append([]NATRule(nil), a.nat...)
	a.mu.RUnlock()
	if len(rules) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	plan, err := a.network.PlanNAT(rules[0].ExternalIP, rules)
	if err != nil {
		return
	}
	_ = a.network.Apply(ctx, plan)
}
