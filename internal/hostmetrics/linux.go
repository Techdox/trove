// Package hostmetrics collects read-only operating-system resource snapshots
// shared by agents that run on the machine they report. It intentionally does
// not inspect processes or files outside the small set of Linux virtual files
// needed for aggregate host metrics.
package hostmetrics

import (
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/techdox/trove/pkg/model"
)

type cpuTimes struct {
	total uint64
	idle  uint64
}

// LinuxSampler reads aggregate metrics from procfs. CPU usage needs two
// samples, so the first collection reports every other available metric and
// establishes the baseline for the next interval.
type LinuxSampler struct {
	includeRootDisk bool
	readFile        func(string) ([]byte, error)
	diskUsage       func(string) (*model.HostResourceUsage, error)

	mu       sync.Mutex
	previous *cpuTimes
}

// NewLinuxSampler creates a sampler for the local Linux kernel. Root-disk
// collection must be disabled inside ordinary containers because statfs("/")
// describes their overlay filesystem, not the machine's root filesystem.
func NewLinuxSampler(includeRootDisk bool) *LinuxSampler {
	return &LinuxSampler{
		includeRootDisk: includeRootDisk,
		readFile:        os.ReadFile,
		diskUsage:       statFSUsage,
	}
}

// Collect returns every metric that could be read plus a joined diagnostic for
// unavailable inputs. Callers should keep the partial snapshot and log the
// error; one missing procfs file must not erase otherwise useful host data.
func (s *LinuxSampler) Collect() (*model.HostMetrics, error) {
	m := &model.HostMetrics{}
	var errs []error
	populated := false

	if b, err := s.readFile("/proc/stat"); err != nil {
		errs = append(errs, fmt.Errorf("cpu: %w", err))
	} else if current, err := parseCPUTimes(b); err != nil {
		errs = append(errs, fmt.Errorf("cpu: %w", err))
	} else if ratio, ok := s.cpuRatio(current); ok {
		m.CPUUsageRatio = &ratio
		populated = true
	}

	if b, err := s.readFile("/proc/loadavg"); err != nil {
		errs = append(errs, fmt.Errorf("load: %w", err))
	} else if load1, load5, load15, err := parseLoadAverage(b); err != nil {
		errs = append(errs, fmt.Errorf("load: %w", err))
	} else {
		m.Load1, m.Load5, m.Load15 = &load1, &load5, &load15
		populated = true
	}

	if b, err := s.readFile("/proc/meminfo"); err != nil {
		errs = append(errs, fmt.Errorf("memory: %w", err))
	} else if memory, err := parseMemory(b); err != nil {
		errs = append(errs, fmt.Errorf("memory: %w", err))
	} else {
		m.Memory = memory
		populated = true
	}

	if b, err := s.readFile("/proc/uptime"); err != nil {
		errs = append(errs, fmt.Errorf("uptime: %w", err))
	} else if uptime, err := parseUptime(b); err != nil {
		errs = append(errs, fmt.Errorf("uptime: %w", err))
	} else {
		m.UptimeSeconds = &uptime
		populated = true
	}

	if s.includeRootDisk {
		if usage, err := s.diskUsage("/"); err != nil {
			errs = append(errs, fmt.Errorf("root disk: %w", err))
		} else {
			m.RootDisk = usage
			populated = true
		}
	}

	if !populated {
		return nil, errors.Join(errs...)
	}
	return m, errors.Join(errs...)
}

// LinuxMetadata returns stable, non-sensitive platform facts for the host
// details drawer. Missing files simply omit their fields.
func LinuxMetadata() map[string]string {
	meta := map[string]string{
		"linux.arch": runtime.GOARCH,
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		if kernel := strings.TrimSpace(string(b)); kernel != "" {
			meta["linux.kernel"] = kernel
		}
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		if name := osReleaseName(b); name != "" {
			meta["linux.os"] = name
		}
	}
	return meta
}

func (s *LinuxSampler) cpuRatio(current cpuTimes) (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.previous
	s.previous = &current
	if previous == nil || current.total <= previous.total || current.idle < previous.idle {
		return 0, false
	}
	totalDelta := current.total - previous.total
	idleDelta := current.idle - previous.idle
	if totalDelta == 0 || idleDelta > totalDelta {
		return 0, false
	}
	return float64(totalDelta-idleDelta) / float64(totalDelta), true
}

func parseCPUTimes(b []byte) (cpuTimes, error) {
	line, _, _ := strings.Cut(string(b), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}, errors.New("missing aggregate cpu line")
	}
	values := make([]uint64, 0, len(fields)-1)
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuTimes{}, fmt.Errorf("parse counter %q: %w", field, err)
		}
		values = append(values, value)
	}
	var total uint64
	for _, value := range values {
		if math.MaxUint64-total < value {
			return cpuTimes{}, errors.New("counter overflow")
		}
		total += value
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4] // iowait is idle time for utilization purposes
	}
	return cpuTimes{total: total, idle: idle}, nil
}

func parseLoadAverage(b []byte) (float64, float64, float64, error) {
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return 0, 0, 0, errors.New("expected three load averages")
	}
	values := [3]float64{}
	for i := range values {
		value, err := strconv.ParseFloat(fields[i], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, 0, 0, fmt.Errorf("invalid load average %q", fields[i])
		}
		values[i] = value
	}
	return values[0], values[1], values[2], nil
}

func parseMemory(b []byte) (*model.HostResourceUsage, error) {
	values := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		key, raw, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
			if value > math.MaxUint64/1024 {
				return nil, fmt.Errorf("%s overflows bytes", key)
			}
			value *= 1024
		}
		values[key] = value
	}
	total, ok := values["MemTotal"]
	if !ok || total == 0 {
		return nil, errors.New("MemTotal is missing or zero")
	}
	available, ok := values["MemAvailable"]
	if !ok {
		available = values["MemFree"] + values["Buffers"] + values["Cached"] + values["SReclaimable"]
		if shmem := values["Shmem"]; shmem < available {
			available -= shmem
		}
	}
	if available > total {
		return nil, errors.New("available memory exceeds total memory")
	}
	return &model.HostResourceUsage{UsedBytes: total - available, TotalBytes: total}, nil
}

func parseUptime(b []byte) (uint64, error) {
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, errors.New("uptime is empty")
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > math.MaxUint64 {
		return 0, fmt.Errorf("invalid uptime %q", fields[0])
	}
	return uint64(seconds), nil
}

func statFSUsage(path string) (*model.HostResourceUsage, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return nil, err
	}
	if stat.Bsize <= 0 {
		return nil, errors.New("invalid filesystem block size")
	}
	blockSize := uint64(stat.Bsize)
	blocks := uint64(stat.Blocks)
	free := uint64(stat.Bfree)
	if blocks == 0 || free > blocks || blocks > math.MaxUint64/blockSize {
		return nil, errors.New("invalid filesystem counters")
	}
	return &model.HostResourceUsage{
		UsedBytes:  (blocks - free) * blockSize,
		TotalBytes: blocks * blockSize,
	}, nil
}

func osReleaseName(b []byte) string {
	values := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		values[key] = value
	}
	if values["PRETTY_NAME"] != "" {
		return values["PRETTY_NAME"]
	}
	return values["NAME"]
}
