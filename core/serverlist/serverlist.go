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
	CityRu     string `json:"city_ru"`
	ServerHost string `json:"server_host"`
	ServerPort int    `json:"server_port"`
	Locked     bool   `json:"locked"`
	PingMS     int64  `json:"ping_ms"` // 0 until measured by real network probe
}

// cityNames maps Remote Settings city codes to real city names.
// Mozilla's vpn-serverlist puts the COUNTRY name in each city's
// "name" field (DE's only "city" is literally named "Germany" with
// code MUC), so the real city exists only as the airport-style
// code — and in the hostname. Anycast countries (US, CA) are
// absent on purpose: they have no single city.
var cityNames = map[string][2]string{
	"MUC":  {"Munich", "Мюнхен"},
	"MXP":  {"Milan", "Милан"},
	"FAOR": {"Johannesburg", "Йоханнесбург"},
	"SPJC": {"Lima", "Лима"},
	"SBSP": {"São Paulo", "Сан-Паулу"},
	"ICN":  {"Seoul", "Сеул"},
	"ACC":  {"Accra", "Аккра"},
	"EZE":  {"Buenos Aires", "Буэнос-Айрес"},
	"MNL":  {"Manila", "Манила"},
	"EGLC": {"London", "Лондон"},
	"LFPB": {"Paris", "Париж"},
	"WSAT": {"Singapore", "Сингапур"},
	"ESSB": {"Stockholm", "Стокгольм"},
	"HEL":  {"Helsinki", "Хельсинки"},
	"AKL":  {"Auckland", "Окленд"},
	"CPH":  {"Copenhagen", "Копенгаген"},
	"SCL":  {"Santiago", "Сантьяго"},
	"OSL":  {"Oslo", "Осло"},
	"EHRD": {"Rotterdam", "Роттердам"},
	"BRU":  {"Brussels", "Брюссель"},
	"KUL":  {"Kuala Lumpur", "Куала-Лумпур"},
	"LETO": {"Madrid", "Мадрид"},
	"VIE":  {"Vienna", "Вена"},
	"YSBK": {"Sydney", "Сидней"},
	"LIS":  {"Lisbon", "Лиссабон"},
	"RJTF": {"Tokyo", "Токио"},
	"MMQT": {"Querétaro", "Керетаро"},
	"SKBO": {"Bogotá", "Богота"},
	"DUB":  {"Dublin", "Дублин"},
	"BKK":  {"Bangkok", "Бангкок"},
	"SOF":  {"Sofia", "София"},
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
		{Code: "DE", Name: "Germany", NameRu: "Германия", City: "Munich (MUC)", CityRu: "Мюнхен (MUC)", ServerHost: "muc139.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "NL", Name: "Netherlands", NameRu: "Нидерланды", City: "Rotterdam (EHRD)", CityRu: "Роттердам (EHRD)", ServerHost: "ehrd229.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "FI", Name: "Finland", NameRu: "Финляндия", City: "Helsinki (HEL)", CityRu: "Хельсинки (HEL)", ServerHost: "hel141.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "SE", Name: "Sweden", NameRu: "Швеция", City: "Stockholm (ESSB)", CityRu: "Стокгольм (ESSB)", ServerHost: "essb127.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "DK", Name: "Denmark", NameRu: "Дания", City: "Copenhagen (CPH)", CityRu: "Копенгаген (CPH)", ServerHost: "cph232.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "BE", Name: "Belgium", NameRu: "Бельгия", City: "Brussels (BRU)", CityRu: "Брюссель (BRU)", ServerHost: "bru148.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "AT", Name: "Austria", NameRu: "Австрия", City: "Vienna (VIE)", CityRu: "Вена (VIE)", ServerHost: "vie63.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "FR", Name: "France", NameRu: "Франция", City: "Paris (LFPB)", CityRu: "Париж (LFPB)", ServerHost: "lfpb115.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "NO", Name: "Norway", NameRu: "Норвегия", City: "Oslo (OSL)", CityRu: "Осло (OSL)", ServerHost: "osl65.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "GB", Name: "United Kingdom", NameRu: "Великобритания", City: "London (EGLC)", CityRu: "Лондон (EGLC)", ServerHost: "eglc860.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "ES", Name: "Spain", NameRu: "Испания", City: "Madrid (LETO)", CityRu: "Мадрид (LETO)", ServerHost: "leto235.m1.fastly-masque.net", ServerPort: 2499, Locked: true, PingMS: 0},
		{Code: "US", Name: "United States", NameRu: "США", City: "United States (US)", CityRu: "США (US)", ServerHost: "us.m1.fastly-masque.net", ServerPort: 2499, Locked: false, PingMS: 0},
	}
}

// DefaultRecommended returns Mozilla's anycast entry point — the REC
// record in Remote Settings. It is a single host whose egress point
// Fastly's routing chooses; it has no country or city and is kept out
// of the country list on purpose.
func DefaultRecommended() Location {
	return Location{
		Code:       "REC",
		Name:       "Recommended",
		NameRu:     "Рекомендуемый",
		City:       "Anycast (REC)",
		CityRu:     "Anycast (REC)",
		ServerHost: "p.m1.fastly-masque.net",
		ServerPort: 2499,
		Locked:     false,
		PingMS:     0,
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
// Fetch returns the country list only; the REC anycast entry is
// available separately via FetchAll.
func Fetch(ctx context.Context) ([]Location, error) {
	list, _, err := FetchAll(ctx)
	return list, err
}

// FetchAll fetches the Remote Settings server list and returns the
// country locations plus the REC anycast location (the fallback
// DefaultRecommended when the record carries no usable server).
func FetchAll(ctx context.Context) ([]Location, Location, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, RemoteSettingsURL, nil)
	if err != nil {
		return nil, DefaultRecommended(), err
	}
	fxa.ApplyFirefoxHeaders(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, DefaultRecommended(), err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, DefaultRecommended(), fmt.Errorf("serverlist HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, DefaultRecommended(), err
	}

	var envelope struct {
		Data []rsRecord `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, DefaultRecommended(), err
	}

	var out []Location
	anycast := DefaultRecommended()
	seen := make(map[string]bool)
	for _, rec := range envelope.Data {
		code := strings.ToUpper(strings.TrimSpace(rec.Code))
		if code == "REC" {
			// The anycast entry: one host, Fastly routing picks the
			// egress. Not a country — kept out of the list.
			for _, city := range rec.Cities {
				taken := false
				for _, srv := range city.Servers {
					if srv.Quarantined || srv.Hostname == "" {
						continue
					}
					port := srv.Port
					if port <= 0 {
						port = 2499
					}
					anycast.ServerHost = srv.Hostname
					anycast.ServerPort = port
					taken = true
					break
				}
				if taken {
					break
				}
			}
			continue
		}
		if code == "" || seen[code] {
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
				cityEN, cityRU := city.Name, nameRu
				if names, ok := cityNames[strings.ToUpper(strings.TrimSpace(city.Code))]; ok {
					cityEN, cityRU = names[0], names[1]
				}
				out = append(out, Location{
					Code:       code,
					Name:       rec.Name,
					NameRu:     nameRu,
					City:       fmt.Sprintf("%s (%s)", cityEN, city.Code),
					CityRu:     fmt.Sprintf("%s (%s)", cityRU, city.Code),
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
	return out, anycast, nil
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
	return MeasureLatenciesProgressive(ctx, locations, dialProbe, nil)
}

// MeasureLatenciesProgressive runs concurrent real network probes and invokes onProgress
// with a sorted snapshot as probes complete, so fast nodes appear in the UI within
// hundreds of milliseconds instead of waiting for unreachable nodes to time out.
func MeasureLatenciesProgressive(
	ctx context.Context,
	locations []Location,
	dialProbe func(ctx context.Context, endpoint string) (time.Duration, error),
	onProgress func(snapshot []Location),
) []Location {
	results := make([]Location, len(locations))
	copy(results, locations)

	var resMu sync.Mutex
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup

	for i := range results {
		wg.Add(1)
		go func(idx int, ep string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			d, err := dialProbe(ctx, ep)
			var ms int64
			if err == nil && d > 0 {
				ms = d.Milliseconds()
				if ms < 1 {
					ms = 1
				}
			}

			resMu.Lock()
			results[idx].PingMS = ms
			if onProgress != nil && ms > 0 {
				snap := make([]Location, len(results))
				copy(snap, results)
				sortFastestFirst(snap)
				resMu.Unlock()
				onProgress(snap)
			} else {
				resMu.Unlock()
			}
		}(i, results[i].Endpoint())
	}
	wg.Wait()

	resMu.Lock()
	final := make([]Location, len(results))
	copy(final, results)
	resMu.Unlock()
	sortFastestFirst(final)
	return final
}
