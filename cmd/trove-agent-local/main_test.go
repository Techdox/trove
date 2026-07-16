package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/techdox/trove/pkg/model"
)

type stubLocalMetricSampler struct {
	metrics *model.HostMetrics
	err     error
}

func (s stubLocalMetricSampler) Collect() (*model.HostMetrics, error) { return s.metrics, s.err }

func TestCollectReportsLinuxHostConditionAndMetrics(t *testing.T) {
	used, total := uint64(4<<30), uint64(16<<30)
	col := &collector{
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		hostname: "nas01",
		metrics: stubLocalMetricSampler{metrics: &model.HostMetrics{
			Memory: &model.HostResourceUsage{UsedBytes: used, TotalBytes: total},
		}},
		listUnits: func(context.Context) ([]byte, error) {
			return []byte(`[{"unit":"docker.service","load":"loaded","active":"active","sub":"running","description":"Docker"}]`), nil
		},
	}

	snaps, err := col.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snaps) != 1 || snaps[0].Host.Hostname != "nas01" ||
		snaps[0].Host.Condition != model.HostConditionNormal || snaps[0].Host.Metrics == nil ||
		snaps[0].Host.Metrics.Memory == nil || snaps[0].Host.Metrics.Memory.UsedBytes != used ||
		snaps[0].Host.Metrics.CPULogicalCount == nil || *snaps[0].Host.Metrics.CPULogicalCount <= 0 ||
		len(snaps[0].Services) != 1 || snaps[0].Services[0].Name != "docker" {
		t.Fatalf("local snapshot = %+v", snaps)
	}
}
