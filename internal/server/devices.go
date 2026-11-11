package server

import (
	"context"
	"time"

	"github.com/gmccollu/woodpile/internal/api"
	"github.com/gmccollu/woodpile/internal/index"
)

// QueryDevices pages the device list in SQL (index.QueryDevices, security
// review N15: the list used to be built whole for every request). sort=rate
// orders by the index's per-minute stats from the start of the previous whole
// minute (the window msgs_per_min uses); the rate shown is the live one where
// the device has sent since start.
func (b *backend) QueryDevices(ctx context.Context, q api.DeviceQuery) (api.DevicePage, error) {
	now := b.now()
	minute := now.UTC().Truncate(time.Minute)
	page, err := b.idx.QueryDevices(ctx, index.DeviceQuery{
		Limit: q.Limit, Offset: q.Offset, Sort: index.DeviceSort(q.Sort), Q: q.Q, Group: q.Group, Ungrouped: q.Ungrouped,
		Hidden: index.HiddenFilter(q.Hidden), RateFrom: minute.Add(-time.Minute),
	})
	if err != nil {
		return api.DevicePage{}, err
	}
	rates, err := b.deviceRates(ctx, now)
	if err != nil {
		return api.DevicePage{}, err
	}
	quiet := b.config().Quiet
	live := b.live.snapshot(now)
	out := api.DevicePage{Total: page.Total, Devices: make([]api.Device, 0, len(page.Devices))}
	for _, d := range page.Devices {
		if d.ID == index.RefusedDeviceID {
			continue
		}
		out.Devices = append(out.Devices, withLive(d, live, rates, now, quiet))
	}
	return out, nil
}

// Groups serves GET /api/groups from the index.
func (b *backend) Groups(ctx context.Context) ([]api.Group, error) {
	gs, err := b.idx.Groups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.Group, len(gs))
	for i, g := range gs {
		out[i] = api.Group{Name: g.Name, Devices: g.Devices}
	}
	return out, nil
}
