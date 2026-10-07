// Package telemetry samples CPU, memory and GPU on request. Nothing here runs on a timer:
// the panel asks only while its tab is open and an engine works, or once on a user action.
package telemetry

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GPU is one NVIDIA GPU as nvidia-smi reports it. Missing readings stay nil.
type GPU struct {
	Name        string   `json:"name"`
	TotalMB     int      `json:"total"`
	UsedMB      int      `json:"used"`
	FreeMB      int      `json:"free"`
	Utilization *float64 `json:"utilization"`
	Temperature *float64 `json:"temperature"`
	PowerWatts  *float64 `json:"power_watts"`
	FanPercent  *float64 `json:"fan_percent"`
}

// RunCommand runs a program and returns its output; tests replace it.
var RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// GPUs queries nvidia-smi; without it (no NVIDIA driver) the list is empty.
func GPUs(ctx context.Context) []GPU {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := RunCommand(ctx, "nvidia-smi", "--query-gpu=name,memory.total,memory.used,memory.free,utilization.gpu,temperature.gpu,power.draw,fan.speed", "--format=csv,noheader,nounits")
	if err != nil {
		return []GPU{}
	}
	return parseGPUs(out)
}

func parseGPUs(out []byte) []GPU {
	gpus := []GPU{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) < 4 {
			continue
		}
		g := GPU{Name: parts[0]}
		g.TotalMB, _ = strconv.Atoi(parts[1])
		g.UsedMB, _ = strconv.Atoi(parts[2])
		g.FreeMB, _ = strconv.Atoi(parts[3])
		for i, dst := range []**float64{&g.Utilization, &g.Temperature, &g.PowerWatts, &g.FanPercent} {
			if len(parts) > 4+i {
				if v, err := strconv.ParseFloat(parts[4+i], 64); err == nil {
					*dst = &v
				}
			}
		}
		gpus = append(gpus, g)
	}
	return gpus
}
