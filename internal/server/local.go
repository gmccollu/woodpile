package server

import (
	"context"

	"github.com/gmccollu/woodshed/internal/api"
)

// In-process views for a caller that runs the server itself (portable mode's
// console): the same data as /api/status and /api/devices, without HTTP, so they
// work whatever the UI's login settings (require_login_local).

// LocalStatus returns what GET /api/status would.
func (s *Server) LocalStatus(ctx context.Context) (api.Status, error) { return s.be.Status(ctx) }

// LocalDevices returns every device, hidden ones included, as GET /api/devices
// lists them.
func (s *Server) LocalDevices(ctx context.Context) ([]api.Device, error) { return s.be.Devices(ctx) }
