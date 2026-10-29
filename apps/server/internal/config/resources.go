package config

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
)

// ContainerResources reads cgroup v2 counters, not another process's heap.
// The brief probe itself is included in these container counters.
func ContainerResources(out io.Writer) error {
	result := map[string]any{}
	for _, name := range []string{"memory.current", "memory.peak", "cpu.stat"} {
		b, err := os.ReadFile("/sys/fs/cgroup/" + name)
		if err != nil {
			return errors.New("cgroup v2 resources unavailable")
		}
		if name == "cpu.stat" {
			cpu := map[string]int64{}
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 {
					value, e := strconv.ParseInt(fields[1], 10, 64)
					if e != nil {
						return errors.New("invalid cgroup counter")
					}
					cpu[fields[0]] = value
				}
			}
			result[name] = cpu
		} else {
			value, e := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
			if e != nil {
				return errors.New("invalid cgroup counter")
			}
			result[name] = value
		}
	}
	return json.NewEncoder(out).Encode(result)
}
