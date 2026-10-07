//go:build !windows

package telemetry

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

func cpuTimes() (idle, total uint64, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0, 0, errors.New("empty /proc/stat")
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, errors.New("unexpected /proc/stat")
	}
	for i, f := range fields[1:] {
		v, _ := strconv.ParseUint(f, 10, 64)
		total += v
		if i == 3 || i == 4 { // idle and iowait
			idle += v
		}
	}
	return idle, total, nil
}

func cpuPercent(ctx context.Context, window time.Duration) (float64, error) {
	idle1, total1, err := cpuTimes()
	if err != nil {
		return 0, err
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(window):
	}
	idle2, total2, err := cpuTimes()
	if err != nil || total2 <= total1 {
		return 0, errors.New("no CPU progress")
	}
	return 100 * (1 - float64(idle2-idle1)/float64(total2-total1)), nil
}

func memory() (total, available uint64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			available = v * 1024
		}
	}
	return total, available, nil
}
