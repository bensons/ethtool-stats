//go:build linux

package main

import (
    "bufio"
    "bytes"
    "flag"
    "fmt"
    "log"
    "net/http"
    "os"
    "strings"
    "time"

    "github.com/gogo/protobuf/proto"
    "github.com/golang/snappy"
    "github.com/prometheus/prometheus/prompb"
    "github.com/safchain/ethtool"
)

var (
    debugEnabled bool
    // Build info set by goreleaser
    version = "dev"
    commit  = "none"
    date    = "unknown"
)

func debugLog(format string, args ...interface{}) {
    if debugEnabled {
        log.Printf("[DEBUG] "+format, args...)
    }
}

func getInterfacesFromARP() ([]string, error) {
    debugLog("Reading /proc/net/arp to discover interfaces")
    file, err := os.Open("/proc/net/arp")
    if err != nil {
        debugLog("Failed to open /proc/net/arp: %v", err)
        return nil, err
    }
    defer file.Close()

    interfaces := map[string]struct{}{}
    scanner := bufio.NewScanner(file)
    isFirst := true

    for scanner.Scan() {
        line := scanner.Text()
        if isFirst {
            isFirst = false // skip header
            continue
        }
        fields := strings.Fields(line)
        if len(fields) > 5 {
            interfaces[fields[5]] = struct{}{}
        }
    }
    if err := scanner.Err(); err != nil {
        debugLog("Error scanning /proc/net/arp: %v", err)
        return nil, err
    }

    ifaces := make([]string, 0, len(interfaces))
    for iface := range interfaces {
        ifaces = append(ifaces, iface)
    }
    debugLog("Discovered %d interface(s): %v", len(ifaces), ifaces)
    return ifaces, nil
}

func pushMetrics(url, iface string, stats map[string]uint64) error {
    debugLog("Pushing %d metrics for interface %s to %s", len(stats), iface, url)

    // Build TimeSeries for each metric
    timeseries := make([]prompb.TimeSeries, 0, len(stats))
    now := time.Now().UnixMilli()

    for k, v := range stats {
        // Normalize metric name
        metric := strings.ReplaceAll(k, "-", "_")
        metric = strings.ReplaceAll(metric, ".", "_")
        metricName := "ethtool_" + metric

        // Create TimeSeries
        ts := prompb.TimeSeries{
            Labels: []prompb.Label{
                {Name: "__name__", Value: metricName},
                {Name: "interface", Value: iface},
            },
            Samples: []prompb.Sample{
                {Value: float64(v), Timestamp: now},
            },
        }
        timeseries = append(timeseries, ts)
    }

    // Create WriteRequest
    writeReq := &prompb.WriteRequest{
        Timeseries: timeseries,
    }

    // Marshal to protobuf
    data, err := proto.Marshal(writeReq)
    if err != nil {
        debugLog("Failed to marshal protobuf: %v", err)
        return err
    }

    // Compress with Snappy
    compressed := snappy.Encode(nil, data)

    // Create HTTP request
    req, err := http.NewRequest("POST", url, bytes.NewReader(compressed))
    if err != nil {
        debugLog("Failed to create HTTP request: %v", err)
        return err
    }

    req.Header.Set("Content-Encoding", "snappy")
    req.Header.Set("Content-Type", "application/x-protobuf")
    req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        debugLog("Failed to push metrics: %v", err)
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        debugLog("Failed to push metrics for %s (HTTP %d)", iface, resp.StatusCode)
        return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
    }

    debugLog("Successfully pushed metrics for %s (HTTP %d)", iface, resp.StatusCode)
    return nil
}

func main() {
    promURL := flag.String("prom", "", "Prometheus Remote Write API endpoint (required)")
    debug := flag.Bool("debug", false, "Enable debug logging")
    showVersion := flag.Bool("version", false, "Show version information")
    flag.Parse()

    if *showVersion {
        fmt.Printf("ethtool-stats %s\n", version)
        fmt.Printf("  commit: %s\n", commit)
        fmt.Printf("  built:  %s\n", date)
        os.Exit(0)
    }

    debugEnabled = *debug

    if *promURL == "" {
        fmt.Println("Usage: ./ethtool-stats -prom=http://localhost:9090/api/v1/write [-debug]")
        os.Exit(1)
    }

    debugLog("Starting ethtool-stats exporter (version: %s, commit: %s)", version, commit)

    debugLog("Prometheus Remote Write API endpoint: %s", *promURL)

    handle, err := ethtool.NewEthtool()
    if err != nil {
        panic(err)
    }
    defer handle.Close()
    debugLog("Ethtool handle initialized successfully")

    for {
        debugLog("Starting collection cycle")
        ifaces, err := getInterfacesFromARP()
        if err == nil {
            for _, iface := range ifaces {
                debugLog("Collecting stats for interface: %s", iface)
                stats, err := handle.Stats(iface)
                if err == nil {
                    pushMetrics(*promURL, iface, stats)
                } else {
                    debugLog("Failed to get stats for %s: %v", iface, err)
                }
            }
        } else {
            debugLog("Failed to get interfaces: %v", err)
        }
        debugLog("Sleeping for 30 seconds")
        time.Sleep(30 * time.Second)
    }
}
