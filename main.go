//go:build linux

package main

import (
    "bufio"
    "flag"
    "fmt"
    "net/http"
    "os"
    "strings"
    "time"

    "github.com/safchain/ethtool"
)

func getInterfacesFromARP() ([]string, error) {
    file, err := os.Open("/proc/net/arp")
    if err != nil {
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
        return nil, err
    }

    ifaces := make([]string, 0, len(interfaces))
    for iface := range interfaces {
        ifaces = append(ifaces, iface)
    }
    return ifaces, nil
}

func pushMetrics(url, iface string, stats map[string]uint64) error {
    var b strings.Builder
    for k, v := range stats {
        metric := strings.ReplaceAll(k, "-", "_")
        metric = strings.ReplaceAll(metric, ".", "_")
        fmt.Fprintf(&b, "ethtool_%s{interface=\"%s\"} %d\n", metric, iface, v)
    }
    req, err := http.NewRequest("POST", url, strings.NewReader(b.String()))
    if err != nil {
        return err
    }
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    return nil
}

func main() {
    promURL := flag.String("prom", "", "Prometheus metrics endpoint (required)")
    flag.Parse()
    if *promURL == "" {
        fmt.Println("Usage: ./ethtool_exporter -prom=http://example.com:9090/metrics/job/ethtool_stats")
        os.Exit(1)
    }

    handle, err := ethtool.NewEthtool()
    if err != nil {
        panic(err)
    }
    defer handle.Close()

    for {
        ifaces, err := getInterfacesFromARP()
        if err == nil {
            for _, iface := range ifaces {
                stats, err := handle.Stats(iface)
                if err == nil {
                    pushMetrics(*promURL, iface, stats)
                }
            }
        }
        time.Sleep(30 * time.Second)
    }
}
