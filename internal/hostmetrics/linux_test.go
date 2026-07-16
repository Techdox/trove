package hostmetrics

import (
	"errors"
	"math"
	"testing"

	"github.com/techdox/trove/pkg/model"
)

func TestLinuxSamplerCollectsPartialThenIntervalCPU(t *testing.T) {
	files := map[string][]byte{
		"/proc/stat":    []byte("cpu  100 0 50 850 0 0 0 0\n"),
		"/proc/loadavg": []byte("0.25 0.50 0.75 1/100 42\n"),
		"/proc/meminfo": []byte("MemTotal: 1024 kB\nMemAvailable: 256 kB\n"),
		"/proc/uptime":  []byte("90061.42 0.00\n"),
	}
	s := &LinuxSampler{
		includeRootDisk: true,
		readFile: func(path string) ([]byte, error) {
			b, ok := files[path]
			if !ok {
				return nil, errors.New("not found")
			}
			return b, nil
		},
		diskUsage: func(string) (*model.HostResourceUsage, error) {
			return &model.HostResourceUsage{UsedBytes: 40, TotalBytes: 100}, nil
		},
	}

	first, err := s.Collect()
	if err != nil {
		t.Fatalf("first collect: %v", err)
	}
	if first.CPUUsageRatio != nil || first.Memory == nil || first.Memory.UsedBytes != 768*1024 ||
		first.RootDisk == nil || first.UptimeSeconds == nil || *first.UptimeSeconds != 90061 ||
		first.Load1 == nil || *first.Load1 != 0.25 {
		t.Fatalf("first metrics = %+v", first)
	}

	files["/proc/stat"] = []byte("cpu  140 0 60 900 0 0 0 0\n")
	second, err := s.Collect()
	if err != nil {
		t.Fatalf("second collect: %v", err)
	}
	if second.CPUUsageRatio == nil || math.Abs(*second.CPUUsageRatio-0.5) > 0.0001 {
		t.Fatalf("cpu ratio = %v, want 0.5", second.CPUUsageRatio)
	}
}

func TestLinuxSamplerKeepsPartialMetricsWithDiagnostics(t *testing.T) {
	s := &LinuxSampler{
		readFile: func(path string) ([]byte, error) {
			if path == "/proc/loadavg" {
				return []byte("1.00 2.00 3.00 1/1 1"), nil
			}
			return nil, errors.New("denied")
		},
	}
	m, err := s.Collect()
	if m == nil || m.Load1 == nil || *m.Load1 != 1 || err == nil {
		t.Fatalf("partial collect = (%+v, %v)", m, err)
	}
}

func TestParseMemoryFallsBackWithoutMemAvailable(t *testing.T) {
	m, err := parseMemory([]byte("MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 200 kB\nSReclaimable: 25 kB\nShmem: 10 kB\n"))
	if err != nil {
		t.Fatalf("parse memory: %v", err)
	}
	wantUsed := uint64(635 * 1024)
	if m.UsedBytes != wantUsed || m.TotalBytes != 1000*1024 {
		t.Fatalf("memory = %+v, want used %d", m, wantUsed)
	}
}

func TestOSReleaseNamePrefersPrettyName(t *testing.T) {
	b := []byte("NAME=Debian\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n")
	if got := osReleaseName(b); got != "Debian GNU/Linux 12 (bookworm)" {
		t.Fatalf("os name = %q", got)
	}
}
