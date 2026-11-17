// Package geoip holds aggregated per-country IPv4 prefix tables used
// by the home-country bypass: flows to addresses of the user's home
// country leave directly instead of through the VPN proxy.
//
// Data lives in data/<cc>.txt (one CIDR per line, aggregated; see
// fetch_data.py for provenance and refresh) and is embedded in the
// binary. Only the selected country's table is parsed, on demand.
package geoip

import (
	"bufio"
	"embed"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
)

//go:embed data
var dataFS embed.FS

// Countries lists the country codes a table is available for.
func Countries() []string {
	entries, err := dataFS.ReadDir("data")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".txt") {
			out = append(out, strings.ToUpper(strings.TrimSuffix(name, ".txt")))
		}
	}
	sort.Strings(out)
	return out
}

type ipRange struct{ lo, hi uint32 }

// Table is one country's merged, sorted prefix ranges.
type Table struct {
	Code   string
	ranges []ipRange
}

// PrefixCount reports how many aggregated prefixes the table holds.
func (t *Table) PrefixCount() int { return len(t.ranges) }

// Contains reports whether ip belongs to the table's country.
// IPv6 and unparseable input never match: the dataset is IPv4-only.
func (t *Table) Contains(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	v := uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
	i := sort.Search(len(t.ranges), func(i int) bool { return t.ranges[i].hi >= v })
	return i < len(t.ranges) && t.ranges[i].lo <= v
}

var (
	cacheMu sync.Mutex
	cache   = map[string]*Table{}
)

// Load returns the (cached) table for a country code, e.g. "RU".
func Load(code string) (*Table, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if t, ok := cache[code]; ok {
		return t, nil
	}
	f, err := dataFS.Open("data/" + strings.ToLower(code) + ".txt")
	if err != nil {
		return nil, fmt.Errorf("geoip: no data for %q", code)
	}
	defer f.Close()
	t := &Table{Code: code}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, n, err := net.ParseCIDR(line)
		if err != nil {
			return nil, fmt.Errorf("geoip %s: bad prefix %q", code, line)
		}
		ip4 := n.IP.To4()
		if ip4 == nil {
			continue
		}
		lo := uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
		ones, _ := n.Mask.Size()
		var hi uint32
		if ones == 0 {
			hi = ^uint32(0)
		} else {
			hi = lo | (^uint32(0) >> ones)
		}
		t.ranges = append(t.ranges, ipRange{lo, hi})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	t.ranges = mergeRanges(t.ranges)
	cache[code] = t
	return t, nil
}

func mergeRanges(rs []ipRange) []ipRange {
	if len(rs) == 0 {
		return rs
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].lo < rs[j].lo })
	out := rs[:1]
	for _, r := range rs[1:] {
		last := &out[len(out)-1]
		if r.lo <= last.hi || (last.hi != ^uint32(0) && r.lo == last.hi+1) {
			if r.hi > last.hi {
				last.hi = r.hi
			}
			continue
		}
		out = append(out, r)
	}
	return out
}
