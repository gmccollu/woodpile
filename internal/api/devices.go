package api

import (
	"cmp"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Device list paging (docs/api.md, GET /api/devices).
const (
	defaultDeviceLimit = 200
	maxDeviceLimit     = 1000
)

// DeviceQuery is GET /api/devices' query.
type DeviceQuery struct {
	Limit, Offset int
	Sort          string // rate (busiest first), name, last_heard (newest first), first_seen (newest first)
	Q             string // case-insensitive substring of id, IP or name
	Group         string
	Ungrouped     bool
	Hidden        string // include, only, exclude
}

// DevicePage is the body of GET /api/devices.
type DevicePage struct {
	Devices []Device `json:"devices"`
	Total   int      `json:"total"` // devices matching the filters, before paging
}

// ParseDeviceQuery reads GET /api/devices' query string.
func ParseDeviceQuery(v url.Values) (DeviceQuery, error) {
	q := DeviceQuery{Limit: defaultDeviceLimit, Sort: "rate", Hidden: "include", Q: v.Get("q"), Group: v.Get("group")}
	for _, p := range []struct {
		name string
		dst  *int
		min  int
	}{{"limit", &q.Limit, 1}, {"offset", &q.Offset, 0}} {
		if s := v.Get(p.name); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < p.min {
				return q, fmt.Errorf("%s must be an integer of at least %d, not %q", p.name, p.min, s)
			}
			*p.dst = n
		}
	}
	q.Limit = min(q.Limit, maxDeviceLimit)
	if s := v.Get("sort"); s != "" {
		switch s {
		case "rate", "name", "last_heard", "first_seen":
			q.Sort = s
		default:
			return q, fmt.Errorf("sort must be rate, name, last_heard or first_seen, not %q", s)
		}
	}
	if s := v.Get("hidden"); s != "" {
		switch s {
		case "include", "only", "exclude":
			q.Hidden = s
		default:
			return q, fmt.Errorf("hidden must be include, only or exclude, not %q", s)
		}
	}
	var err error
	if q.Ungrouped, err = parseBoolParam(v, "ungrouped"); err != nil {
		return q, err
	}
	if q.Group != "" && q.Ungrouped {
		return q, fmt.Errorf("group and ungrouped=true are alternatives; send one")
	}
	return q, nil
}

// pageDevices filters, sorts and pages ds (every device, normalized).
func pageDevices(ds []Device, q DeviceQuery) DevicePage {
	needle := strings.ToLower(q.Q)
	out := make([]Device, 0, len(ds))
	for _, d := range ds {
		switch {
		case q.Hidden == "only" && !d.Hidden, q.Hidden == "exclude" && d.Hidden:
			continue
		case q.Group != "" && d.Group != q.Group, q.Ungrouped && d.Group != "":
			continue
		case needle != "" && !strings.Contains(strings.ToLower(d.ID), needle) && !strings.Contains(strings.ToLower(d.IP), needle) &&
			!strings.Contains(strings.ToLower(d.Name), needle):
			continue
		}
		out = append(out, d)
	}
	byID := func(a, b Device) int { return strings.Compare(a.ID, b.ID) }
	slices.SortStableFunc(out, func(a, b Device) int {
		var c int
		switch q.Sort {
		case "name":
			an, bn := a.Name, b.Name
			if an == "" {
				an = a.ID
			}
			if bn == "" {
				bn = b.ID
			}
			c = cmp.Compare(strings.ToLower(an), strings.ToLower(bn))
		case "last_heard":
			c = b.LastHeard.Compare(a.LastHeard)
		case "first_seen":
			c = b.FirstSeen.Compare(a.FirstSeen)
		default:
			c = cmp.Compare(b.MsgsPerMin, a.MsgsPerMin)
		}
		if c == 0 {
			c = byID(a, b)
		}
		return c
	})
	page := DevicePage{Total: len(out), Devices: []Device{}}
	if q.Offset < len(out) {
		page.Devices = out[q.Offset:min(q.Offset+q.Limit, len(out))]
	}
	return page
}

func (s *Server) getDevices(w http.ResponseWriter, r *http.Request) {
	q, err := ParseDeviceQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ds, err := s.backend.Devices(r.Context())
	if err != nil {
		s.backendError(w, r, err)
		return
	}
	for i := range ds {
		ds[i].normalize()
	}
	writeJSON(w, http.StatusOK, pageDevices(ds, q))
}
