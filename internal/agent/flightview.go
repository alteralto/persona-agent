package agent

import (
	"sync"
	"time"
)

// FlightView is what the control panel shows of the flights: the one under way, else the last.
type FlightView struct {
	Flying  bool    `json:"flying"`
	Style   string  `json:"style,omitempty"` // normal, low, tricks, route; wingman beside a player
	Phase   string  `json:"phase,omitempty"` // the fork's flyStatus phase
	Reason  string  `json:"reason,omitempty"`
	Mode    string  `json:"mode,omitempty"` // the pilot's mode while flying (climb, cruise, ...)
	Speed   float64 `json:"speed"`
	Dist    float64 `json:"distance"` // to the target, blocks
	Rockets int     `json:"rockets"`
	Seconds int     `json:"seconds"`
	Solver  bool    `json:"solver"`  // the physics solver was asked for at take-off
	Flights int     `json:"flights"` // flights ended since the agent started
}

// flightView is written by the agent's loop and read by the panel's requests.
type flightView struct {
	mu sync.Mutex
	v  FlightView
}

func (f *flightView) set(fn func(*FlightView)) {
	f.mu.Lock()
	fn(&f.v)
	f.mu.Unlock()
}

// Status is the agent's state for the control panel (GET /status on the voice port).
func (a *Agent) Status() any {
	a.view.mu.Lock()
	defer a.view.mu.Unlock()
	return struct {
		Flight FlightView `json:"flight"`
	}{a.view.v}
}

func (a *Agent) flightStyle() string {
	if !a.flyPoint {
		return "wingman"
	}
	return a.trackStyle
}

// viewFlight records one flyStatus reading.
func (a *Agent) viewFlight(phase, reason, mode string, speed, dist float64, rockets int) {
	ended := phase == "done" || phase == "failed" || phase == "idle"
	a.view.set(func(v *FlightView) {
		if ended && v.Flying {
			v.Flights++
		}
		v.Flying = !ended
		v.Style, v.Phase, v.Reason, v.Rockets = a.flightStyle(), phase, reason, rockets
		v.Mode, v.Speed, v.Dist = mode, speed, dist
		if ended {
			v.Mode, v.Speed = "", 0
		}
		v.Seconds = int(time.Since(a.flightStarted).Seconds())
		v.Solver = a.cfg.Solver && !a.noFlySettings
	})
}
