// Package health runs independent readiness and liveness probes for services.
package health

import (
	"fmt"
	"sync"
	"time"

	"github.com/kranz-org/kranz/internal/config"
	"github.com/kranz-org/kranz/pkg/ringbuffer"
)

// Checker owns the monitoring goroutines for all configured services.
type Checker struct {
	services              map[string]*ServiceHealth
	mu                    sync.RWMutex
	stopCh                map[string]chan struct{}
	detectedPortsProvider func(string) []int
}

// SetDetectedPortsProvider supplies the latest runtime listener snapshot for a
// service. Dynamic probes resolve it immediately before every attempt.
func (hc *Checker) SetDetectedPortsProvider(provider func(string) []int) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.detectedPortsProvider = provider
}

// ProbeState is the last observed outcome of one probe kind. It exists so a
// reader can answer "why is readiness failing" with the endpoint that was
// probed and the error it returned, instead of grepping the history strings.
type ProbeState struct {
	Configured          bool      `json:"configured"`
	Target              string    `json:"target,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	LastAttempt         time.Time `json:"last_attempt,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
}

// ServiceHealth stores the synchronized probe state for one service.
type ServiceHealth struct {
	mu         sync.RWMutex
	Ready      bool
	Alive      bool
	History    *ringbuffer.RingBuffer
	ReadySince time.Time
	LastCheck  time.Time
	readiness  ProbeState
	liveness   ProbeState
}

// Probes returns copies of the readiness and liveness probe states.
func (sh *ServiceHealth) Probes() (readiness, liveness ProbeState) {
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	return sh.readiness, sh.liveness
}

// recordProbe stores one probe attempt while the caller holds mu.
func (state *ProbeState) recordProbe(target string, at time.Time, err error) {
	state.Configured = true
	state.LastAttempt = at
	if target != "" {
		state.Target = target
	}
	if err == nil {
		state.LastError = ""
		state.ConsecutiveFailures = 0
		return
	}
	state.LastError = err.Error()
	state.ConsecutiveFailures++
}

// describeTarget names what a probe actually contacts, after any detected-port
// resolution, so the reported target is the one that was tried.
func describeTarget(cfg *config.CheckConfig) string {
	if cfg == nil {
		return ""
	}
	switch cfg.Type {
	case config.CheckHTTP:
		return cfg.URL
	case config.CheckTCP:
		if cfg.Port == 0 {
			return "tcp"
		}
		return fmt.Sprintf("tcp://127.0.0.1:%d", cfg.Port)
	case config.CheckCommand:
		return cfg.Command
	default:
		return string(cfg.Type)
	}
}

// IsReady returns the latest readiness result.
func (sh *ServiceHealth) IsReady() bool {
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	return sh.Ready
}

// IsAlive returns the latest liveness result.
func (sh *ServiceHealth) IsAlive() bool {
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	return sh.Alive
}

// GetReadySince returns when readiness last transitioned to success.
func (sh *ServiceHealth) GetReadySince() time.Time {
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	return sh.ReadySince
}

// GetLastCheck returns the time of the latest liveness probe.
func (sh *ServiceHealth) GetLastCheck() time.Time {
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	return sh.LastCheck
}

// setReady updates readiness while the caller holds mu.
func (sh *ServiceHealth) setReady(ready bool) {
	sh.Ready = ready
	if ready {
		sh.ReadySince = time.Now()
	}
}

// setAlive updates liveness and its timestamp while the caller holds mu.
func (sh *ServiceHealth) setAlive(alive bool) {
	sh.Alive = alive
	sh.LastCheck = time.Now()
}

// NewChecker creates an empty health monitor.
func NewChecker() *Checker {
	return &Checker{
		services: make(map[string]*ServiceHealth),
		stopCh:   make(map[string]chan struct{}),
	}
}

// StartMonitoring replaces any existing monitor for a service and starts its probes.
func (hc *Checker) StartMonitoring(name string, checkCfg *config.HealthCheckConfig) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Reconfiguration must not leave the previous monitor running.
	if ch, ok := hc.stopCh[name]; ok {
		close(ch)
	}

	health := &ServiceHealth{
		Ready:   false,
		Alive:   false,
		History: ringbuffer.New(50),
	}

	hc.services[name] = health

	if checkCfg == nil {
		// Missing probes are successful by definition.
		health.mu.Lock()
		health.setReady(true)
		health.setAlive(true)
		health.History.Write(formatEvent(time.Now(), "No health check configured — assuming healthy"))
		health.mu.Unlock()
		return
	}

	stopCh := make(chan struct{})
	hc.stopCh[name] = stopCh
	if checkCfg.Readiness == nil {
		health.mu.Lock()
		health.setReady(true)
		health.mu.Unlock()
	} else {
		health.mu.Lock()
		health.readiness = ProbeState{Configured: true, Target: describeTarget(checkCfg.Readiness)}
		health.mu.Unlock()
	}
	if checkCfg.Liveness == nil {
		health.mu.Lock()
		health.setAlive(true)
		health.mu.Unlock()
	} else {
		// A running process is considered alive until the configured failure
		// threshold is reached. LastCheck remains zero until the first probe.
		health.mu.Lock()
		health.Alive = true
		health.liveness = ProbeState{Configured: true, Target: describeTarget(checkCfg.Liveness)}
		health.mu.Unlock()
	}

	// Readiness runs until its first success.
	if checkCfg.Readiness != nil {
		go hc.runReadinessCheck(name, checkCfg.Readiness, health, stopCh)
	}

	// Liveness continues for the lifetime of the service.
	if checkCfg.Liveness != nil {
		go hc.runLivenessCheck(name, checkCfg.Liveness, health, stopCh)
	}
}

// runReadinessCheck probes until readiness succeeds or monitoring is cancelled.
func (hc *Checker) runReadinessCheck(name string, cfg *config.CheckConfig, health *ServiceHealth, stopCh chan struct{}) {
	if !waitInitialDelay(cfg.InitialDelay, stopCh) {
		return
	}
	ticker := time.NewTicker(checkInterval(cfg.Interval))
	defer ticker.Stop()

	for {
		target, err := hc.executeCheck(name, cfg)
		now := time.Now()
		if err == nil {
			health.mu.Lock()
			health.setReady(true)
			health.readiness.recordProbe(target, now, nil)
			health.History.Write(formatEvent(now, "Readiness passed ✓"))
			health.mu.Unlock()
			return
		}
		health.mu.Lock()
		health.readiness.recordProbe(target, now, err)
		health.History.Write(formatEvent(now, "Readiness failed: "+err.Error()))
		health.mu.Unlock()

		select {
		case <-stopCh:
			return
		case <-ticker.C:
		}
	}
}

// runLivenessCheck continuously updates liveness with failure-threshold semantics.
func (hc *Checker) runLivenessCheck(name string, cfg *config.CheckConfig, health *ServiceHealth, stopCh chan struct{}) {
	if !waitInitialDelay(cfg.InitialDelay, stopCh) {
		return
	}
	ticker := time.NewTicker(checkInterval(cfg.Interval))
	defer ticker.Stop()

	failCount := 0

	for {
		target, err := hc.executeCheck(name, cfg)
		now := time.Now()
		health.mu.Lock()
		health.LastCheck = now
		health.liveness.recordProbe(target, now, err)
		if err == nil {
			failCount = 0
			health.Alive = true
			health.History.Write(formatEvent(now, "Liveness passed ✓"))
		} else {
			failCount++
			health.History.Write(formatEvent(now, "Liveness failed: "+err.Error()))
			if failCount >= failureThreshold(cfg.FailureThreshold) {
				health.Alive = false
				health.History.Write(formatEvent(now, fmt.Sprintf("UNHEALTHY: %d consecutive failures", failCount)))
			}
		}
		health.mu.Unlock()

		select {
		case <-stopCh:
			return
		case <-ticker.C:
		}
	}
}

func waitInitialDelay(delay time.Duration, stopCh <-chan struct{}) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-stopCh:
		return false
	}
}

func failureThreshold(value int) int {
	if value <= 0 {
		return 3
	}
	return value
}

func checkInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 5 * time.Second
	}
	return interval
}

// StopMonitoring cancels and removes one service monitor.
func (hc *Checker) StopMonitoring(name string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if ch, ok := hc.stopCh[name]; ok {
		close(ch)
		delete(hc.stopCh, name)
	}
	delete(hc.services, name)
}

// GetHealth returns the synchronized health state for a service.
func (hc *Checker) GetHealth(name string) *ServiceHealth {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	return hc.services[name]
}

// StopAll cancels every active monitor.
func (hc *Checker) StopAll() {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	for name, ch := range hc.stopCh {
		close(ch)
		delete(hc.stopCh, name)
	}
	hc.services = make(map[string]*ServiceHealth)
}

// formatEvent creates a timestamped entry for the bounded health history.
func formatEvent(t time.Time, msg string) string {
	return t.Format("15:04:05") + "  " + msg
}
