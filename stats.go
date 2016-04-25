package main

import (
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/alecthomas/log4go"
	"github.com/cactus/go-statsd-client/statsd"
	"github.com/fsouza/go-dockerclient"
)

var (
	checking     map[string]bool
	logger       log4go.Logger
	statsdClient statsd.Statter
)

func sendMetrix(appID string, metrix map[string]uint64) {
	logger.Debug("Name: %s, CPUPercent: %d, MemUsage: %d, MaxMemUsage: %d, MemLimit: %d, MemPercent: %d",
		appID, metrix["cpuPercent"], metrix["memUsage"],
		metrix["maxMemUsage"], metrix["memLimit"], metrix["memPercent"])

	for k, v := range metrix {
		statsdClient.Gauge(appID+"."+k, int64(v), 1.0)
	}
}

func statsContainer(client *docker.Client, name, cID, appID string) {
	errC := make(chan error, 1)
	statsC := make(chan *docker.Stats)
	done := make(chan bool)

	go func() {
		errC <- client.Stats(docker.StatsOptions{
			ID: cID, Stats: statsC, Stream: true, Done: done,
		})
		close(errC)
	}()

	for {
		stats, ok := <-statsC
		if !ok {
			break
		}

		cpuUsage := stats.CPUStats.CPUUsage.TotalUsage
		systemUsage := stats.CPUStats.SystemCPUUsage

		cpuPercent := 0.0
		cpuDelta := float64(cpuUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
		systemDelta := float64(systemUsage - stats.PreCPUStats.SystemCPUUsage)

		if cpuDelta > 0.0 && systemDelta > 0.0 {
			cpuPercent = (cpuDelta / systemDelta) * float64(len(stats.CPUStats.CPUUsage.PercpuUsage)) * 100.0
		}

		memPercent := float64(stats.MemoryStats.Usage) / float64(stats.MemoryStats.Limit) * 100.0

		sendMetrix(appID, map[string]uint64{
			"cpuPercent":  uint64(cpuPercent * 100.0), // scale percent by 100
			"memUsage":    stats.MemoryStats.Usage,
			"memLimit":    stats.MemoryStats.Limit,
			"maxMemUsage": stats.MemoryStats.MaxUsage,
			"memPercent":  uint64(memPercent * 100.0),
		})
	}

	delete(checking, cID)

	logger.Debug("stop checking container: %s, name: %s", cID, name)

	if err := <-errC; err != nil {
		logger.Error("get %s stats error, %s", cID, err)
		return
	}
}

func checkConteiners() {
	endpoint := "unix:///var/run/docker.sock"
	client, newErr := docker.NewClient(endpoint)
	if newErr != nil {
		logger.Error("new docker client err, ", newErr.Error())
		return
	}

	opts := docker.ListContainersOptions{
		Filters: map[string][]string{"status": {"running"}},
	}
	containers, listErr := client.ListContainers(opts)
	if listErr != nil {
		logger.Error("list containers err, ", listErr.Error())
		return
	}

	for _, cnt := range containers {
		cID := cnt.ID
		if chk, exists := checking[cID]; exists {
			if chk {
				logger.Debug("container %s already in checking", cID)
			} else {
				logger.Debug("container %s already in skiping", cID)
			}
			continue
		}

		container, err := client.InspectContainer(cID)
		if err != nil {
			logger.Error("inspect container %s error, %s", cID, err.Error())
			continue
		}

		appID := ""
		for _, env := range container.Config.Env {
			if idx := strings.Index(env, "MARATHON_APP_ID="); idx == 0 {
				appID = strings.Trim(env[len("MARATHON_APP_ID="):], "/ ")
				break
			}
		}

		// not marathon app
		if appID == "" {
			logger.Debug("%s is not marathon app, skip checking", container.Name)
			checking[cID] = false
			continue
		}

		logger.Debug("checking container: %s, name: %s", cID, container.Name)

		go statsContainer(client, container.Name, cID, appID)

		checking[cID] = true
	}
}

func main() {
	runtime.GOMAXPROCS(1)

	loglevel := log4go.INFO
	if os.Getenv("LOG_LEVEL") == "debug" {
		loglevel = log4go.DEBUG
	}

	logger = make(log4go.Logger)
	w := log4go.NewConsoleLogWriter()
	w.SetFormat("[%D %T] [%L] %M")
	logger.AddFilter("stdout", loglevel, w)

	checking = make(map[string]bool)

	addr := os.Getenv("STATSD_ADDR")
	if addr == "" {
		logger.Critical("no stats address found")
		logger.Close()
		return
	}

	prefix := os.Getenv("STATSD_PREFIX")
	if prefix == "" {
		prefix = "statsd"
	}

	var err error
	statsdClient, err = statsd.NewClient(addr, prefix)
	if err != nil {
		logger.Critical("new statsd client error, addr is %s, %s", addr, err.Error())
		logger.Close()
		return
	}
	defer statsdClient.Close()

	logger.Info("setup statsd client to %s", addr)

	for {
		logger.Debug("listing all containers")

		checkConteiners()
		time.Sleep(5000 * time.Millisecond)
	}
}
