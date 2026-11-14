// Package serverlist fetches Mozilla's Remote Settings vpn-serverlist collection,
// unlocks all locations (ignoring Firefox's locked flag), provides Russian country
// names, and measures real 1-RTT latency from the device to each Fastly node.
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

var countryNamesRu = map[string]string{
	"DE": "Германия",
	"NL": "Нидерланды",
	"FI": "Финляндия",
	"SE": "Швеция",
	"DK": "Дания",
	"BE": "Бельгия",
	"AT": "Австрия",
	"FR": "Франция",
	"NO": "Норвегия",
	"GB": "Великобритания",
	"IT": "Италия",
	"BG": "Болгария",
	"IE": "Ирландия",
	"ES": "Испания",
	"PT": "Португалия",
	"CH": "Швейцария",
	"PL": "Польша",
	"CZ": "Чехия",
	"US": "США",
	"CA": "Канада",
	"SG": "Сингапур",
	"JP": "Япония",
	"KR": "Южная Корея",
	"AU": "Австралия",
	"NZ": "Новая Зеландия",
	"BR": "Бразилия",
	"AR": "Аргентина",
	"CL": "Чили",
	"CO": "Колумбия",
	"PE": "Перу",
	"MX": "Мексика",
	"ZA": "ЮАР",
	"GH": "Гана",
	"TH": "Таиланд",
	"MY": "Малайзия",
	"PH": "Филиппины",
}

type Location struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	NameRu     string `json:"name_ru"`
	City       string `json:"city"`
	ServerHost string `json:"server_host"`
	ServerPort int    `json:"server_port"`
	Locked     bool   `json:"locked"`
	PingMS     int64  `json:"ping_ms"` // 0 until measured by real network probe
}

func (l Location) Endpoint() string {
	port := l.ServerPort
	if port <= 0 {
		port = 2499
	}
	return fmt.Sprintf("%s:%d", l.ServerHost, port)
}

func DefaultLocations() []Location {
	return []Location{
		{Code: "DE", Name: "Germany", NameRu: "Германия", City: "Munich (MUC)", ServerHost: "muc139.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "NL", Name: "Netherlands", NameRu: "Нидерланды", City: "Rotterdam (EHRD)", ServerHost: "ehrd229.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "FI", Name: "Finland", NameRu: "Финляндия", City: "Helsinki (HEL)", ServerHost: "hel141.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "SE", Name: "Sweden", NameRu: "Швеция", City: "Stockholm (ESSB)", ServerHost: "essb127.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "DK", Name: "Denmark", NameRu: "Дания", City: "Copenhagen (CPH)", ServerHost: "cph232.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "BE", Name: "Belgium", NameRu: "Бельгия", City: "Brussels (BRU)", ServerHost: "bru148.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "AT", Name: "Austria", NameRu: "Австрия", City: "Vienna (VIE)", ServerHost: "vie63.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "FR", Name: "France", NameRu: "Франция", City: "Paris (LFPB)", ServerHost: "lfpb115.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "NO", Name: "Norway", NameRu: "Норвегия", City: "Oslo (OSL)", ServerHost: "osl65.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "GB", Name: "United Kingdom", NameRu: "Великобритания", City: "London (EGLC)", ServerHost: "eglc860.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "ES", Name: "Spain", NameRu: "Испания", City: "Madrid (LETO)", ServerHost: "leto235.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "US", Name: "United States", NameRu: "США", City: "United States (US)", ServerHost: "us.m1.fastly-masque.net", ServerPort: 2499, Locked: false, PingMS: 0},
	}
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

// Fetch retrieves all non-quarantined Mozilla Fastly proxy locations with PingMS = 0 (unmeasured).
func Fetch(ctx context.Context) ([]Location, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, RemoteSettingsURL, nil)
	if err != nil {
		return nil, err
	}
	fxa.ApplyFirefoxHeaders(req)

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
				nameRu := countryNamesRu[code]
				if nameRu == "" {
					nameRu = rec.Name
				}
				out = append(out, Location{
					Code:       code,
					Name:       rec.Name,
					NameRu:     nameRu,
					City:       fmt.Sprintf("%s (%s)", city.Name, city.Code),
					ServerHost: srv.Hostname,
					ServerPort: port,
					Locked:     rec.Locked,
					PingMS:     0,
				})
				break
			}
			if seen[code] {
				break
			}
		}
	}

	sortFastestFirst(out)
	return out, nil
}

func sortFastestFirst(locs []Location) {
	sort.SliceStable(locs, func(i, j int) bool {
		aOk := locs[i].PingMS > 0
		bOk := locs[j].PingMS > 0
		if aOk != bOk {
			return aOk
		}
		if aOk && locs[i].PingMS != locs[j].PingMS {
			return locs[i].PingMS < locs[j].PingMS
		}
		return locs[i].NameRu < locs[j].NameRu
	})
}

// MeasureLatencies runs concurrent real network probes using dialProbe and sorts locations by real measured RTT.
func MeasureLatencies(ctx context.Context, locations []Location, dialProbe func(ctx context.Context, endpoint string) (time.Duration, error)) []Location {
	results := make([]Location, len(locations))
	copy(results, locations)

	sem := make(chan struct{}, 10)
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
				ms := d.Milliseconds()
				if ms < 1 {
					ms = 1
				}
				results[idx].PingMS = ms
			} else {
				results[idx].PingMS = 0
			}
		}(i)
	}
	wg.Wait()

	sortFastestFirst(results)
	return results
}
