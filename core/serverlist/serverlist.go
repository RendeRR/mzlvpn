// Package serverlist fetches Mozilla's Remote Settings vpn-serverlist collection,
// unlocks all locations (ignoring Firefox's locked flag), and measures real
// tunnel latency over HTTP/2 CONNECT.
package serverlist

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"mozillavpnlite/core/fxa"
)

const RemoteSettingsURL = "https://firefox.settings.services.mozilla.com/v1/buckets/main/collections/vpn-serverlist/records"

type Location struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	City       string `json:"city"`
	ServerHost string `json:"server_host"`
	ServerPort int    `json:"server_port"`
	Locked     bool   `json:"locked"`
	PingMS     int64  `json:"ping_ms"`
}

func (l Location) Endpoint() string {
	port := l.ServerPort
	if port <= 0 {
		port = 443
	}
	return fmt.Sprintf("%s:%d", l.ServerHost, port)
}

type rsRecord struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Locked bool   `json:"locked"`
	Cities []struct {
		Name    string `json:"name"`
		Code    string `json:"code"`
		Servers []struct {
			Hostname    string `json:"hostname"`
			Port        int    `json:"port"`
			Quarantined bool   `json:"quarantined"`
		} `json:"servers"`
	} `json:"cities"`
}

// Fetch retrieves all non-quarantined Mozilla Fastly proxy locations.
func Fetch(ctx context.Context) ([]Location, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, RemoteSettingsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fxa.FirefoxUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("serverlist HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Data []rsRecord `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}

	var out []Location
	seen := make(map[string]bool)
	for _, rec := range envelope.Data {
		code := strings.ToUpper(strings.TrimSpace(rec.Code))
		if code == "" || code == "REC" || seen[code] {
			continue
		}
		for _, city := range rec.Cities {
			for _, srv := range city.Servers {
				if srv.Quarantined || srv.Hostname == "" {
					continue
				}
				port := srv.Port
				if port <= 0 {
					port = 2499
				}
				seen[code] = true
				out = append(out, Location{
					Code:       code,
					Name:       rec.Name,
					City:       fmt.Sprintf("%s (%s)", city.Name, city.Code),
					ServerHost: srv.Hostname,
					ServerPort: port,
					Locked:     rec.Locked,
				})
				break
			}
			if seen[code] {
				break
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// MeasureLatencies runs concurrent probes using dialProbe and sorts locations fastest-first.
func MeasureLatencies(ctx context.Context, locations []Location, dialProbe func(ctx context.Context, endpoint string) (time.Duration, error)) []Location {
	results := make([]Location, len(locations))
	copy(results, locations)

	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup

	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			d, err := dialProbe(ctx, results[idx].Endpoint())
			if err == nil && d > 0 {
				results[idx].PingMS = d.Milliseconds()
			}
		}(i)
	}
	wg.Wait()

	sort.SliceStable(results, func(i, j int) bool {
		aOk := results[i].PingMS > 0
		bOk := results[j].PingMS > 0
		if aOk != bOk {
			return aOk
		}
		if aOk && results[i].PingMS != results[j].PingMS {
			return results[i].PingMS < results[j].PingMS
		}
		return results[i].Name < results[j].Name
	})
	return results
}
