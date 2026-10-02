package extract

import (
	"regexp"
	"strings"
)

type airport struct {
	City string
	TZ   string
}

// airports maps IATA codes to a display city and IANA zone. Flight times in
// mail are local to the airport, and offsets embedded in mail are not
// trusted (Jetstar labelled an Adelaide summer departure +10:00).
var airports = map[string]airport{
	// Australia
	"SYD": {"Sydney", "Australia/Sydney"}, "MEL": {"Melbourne", "Australia/Melbourne"},
	"AVV": {"Avalon", "Australia/Melbourne"}, "BNE": {"Brisbane", "Australia/Brisbane"},
	"OOL": {"Gold Coast", "Australia/Brisbane"}, "BNK": {"Ballina", "Australia/Sydney"},
	"CBR": {"Canberra", "Australia/Sydney"}, "ADL": {"Adelaide", "Australia/Adelaide"},
	"PER": {"Perth", "Australia/Perth"}, "HBA": {"Hobart", "Australia/Hobart"},
	"LST": {"Launceston", "Australia/Hobart"}, "DRW": {"Darwin", "Australia/Darwin"},
	"ASP": {"Alice Springs", "Australia/Darwin"}, "CNS": {"Cairns", "Australia/Brisbane"},
	"TSV": {"Townsville", "Australia/Brisbane"}, "MCY": {"Sunshine Coast", "Australia/Brisbane"},
	"MKY": {"Mackay", "Australia/Brisbane"}, "ROK": {"Rockhampton", "Australia/Brisbane"},
	"PPP": {"Proserpine", "Australia/Brisbane"}, "HTI": {"Hamilton Island", "Australia/Brisbane"},
	"CFS": {"Coffs Harbour", "Australia/Sydney"}, "NTL": {"Newcastle", "Australia/Sydney"},
	"PQQ": {"Port Macquarie", "Australia/Sydney"}, "ABX": {"Albury", "Australia/Sydney"},
	"WGA": {"Wagga Wagga", "Australia/Sydney"}, "DBO": {"Dubbo", "Australia/Sydney"},
	"BME": {"Broome", "Australia/Perth"}, "KTA": {"Karratha", "Australia/Perth"},
	"ULU": {"Uluru", "Australia/Darwin"}, "AYQ": {"Uluru", "Australia/Darwin"},
	// New Zealand and Pacific
	"AKL": {"Auckland", "Pacific/Auckland"}, "WLG": {"Wellington", "Pacific/Auckland"},
	"CHC": {"Christchurch", "Pacific/Auckland"}, "ZQN": {"Queenstown", "Pacific/Auckland"},
	"NAN": {"Nadi", "Pacific/Fiji"}, "HNL": {"Honolulu", "Pacific/Honolulu"},
	"NOU": {"Noumea", "Pacific/Noumea"}, "VLI": {"Port Vila", "Pacific/Efate"},
	// Asia and beyond
	"DPS": {"Denpasar", "Asia/Makassar"}, "SIN": {"Singapore", "Asia/Singapore"},
	"KUL": {"Kuala Lumpur", "Asia/Kuala_Lumpur"}, "BKK": {"Bangkok", "Asia/Bangkok"},
	"HKT": {"Phuket", "Asia/Bangkok"}, "HKG": {"Hong Kong", "Asia/Hong_Kong"},
	"NRT": {"Tokyo", "Asia/Tokyo"}, "HND": {"Tokyo", "Asia/Tokyo"}, "KIX": {"Osaka", "Asia/Tokyo"},
	"ICN": {"Seoul", "Asia/Seoul"}, "TPE": {"Taipei", "Asia/Taipei"}, "MNL": {"Manila", "Asia/Manila"},
	"SGN": {"Ho Chi Minh City", "Asia/Ho_Chi_Minh"}, "HAN": {"Hanoi", "Asia/Bangkok"},
	"DOH": {"Doha", "Asia/Qatar"}, "DXB": {"Dubai", "Asia/Dubai"},
	"LHR": {"London", "Europe/London"}, "LGW": {"London", "Europe/London"},
	"CDG": {"Paris", "Europe/Paris"}, "FRA": {"Frankfurt", "Europe/Berlin"},
	"AMS": {"Amsterdam", "Europe/Amsterdam"}, "LAX": {"Los Angeles", "America/Los_Angeles"},
	"SFO": {"San Francisco", "America/Los_Angeles"}, "JFK": {"New York", "America/New_York"},
	"DFW": {"Dallas", "America/Chicago"}, "YVR": {"Vancouver", "America/Vancouver"},
}

// cityZones resolves airport/city names used in mail ("Sydney (Kingsford
// Smith)", "Ballina Byron") by their leading words.
var cityZones = func() map[string]string {
	m := map[string]string{"ballina byron": "Australia/Sydney", "byron bay": "Australia/Sydney", "kingsford smith": "Australia/Sydney"}
	for _, a := range airports {
		m[strings.ToLower(a.City)] = a.TZ
	}
	return m
}()

// zoneForCity returns the zone of a known city name, or "".
func zoneForCity(name string) string {
	n := strings.ToLower(normalize(name))
	best := ""
	bestLen := 0
	for city, tz := range cityZones {
		if (n == city || strings.HasPrefix(n, city+" ") || strings.HasPrefix(n, city+",") || strings.HasPrefix(n, city+"(")) && len(city) > bestLen {
			best, bestLen = tz, len(city)
		}
	}
	return best
}

// iataForCity returns a known airport code for a city name, or "".
func iataForCity(name string) string {
	tz := zoneForCity(name)
	if tz == "" {
		return ""
	}
	n := strings.ToLower(normalize(name))
	for code, a := range airports {
		if strings.HasPrefix(n, strings.ToLower(a.City)) {
			return code
		}
	}
	return ""
}

var reAUState = regexp.MustCompile(`\b(NSW|ACT|VIC|QLD|SA|WA|TAS|NT)\b,?\s*\d{4}\b`)

var stateZones = map[string]string{
	"NSW": "Australia/Sydney", "ACT": "Australia/Sydney", "VIC": "Australia/Melbourne",
	"TAS": "Australia/Hobart", "QLD": "Australia/Brisbane", "SA": "Australia/Adelaide",
	"WA": "Australia/Perth", "NT": "Australia/Darwin",
}

// zoneForAddress returns the zone of an Australian address with a state and
// postcode ("46 Grote St, Adelaide, SA, 5000"), or "".
func zoneForAddress(addr string) string {
	if m := reAUState.FindStringSubmatch(addr); m != nil {
		return stateZones[m[1]]
	}
	return ""
}
