package agent

import "time"

// ShortGames makes the games' timings fit a test; the returned func puts them back.
func ShortGames(riddle, hint, quiet, race, setup time.Duration) func() {
	r, h, q, rc, s := riddleFor, riddleHint, heatQuiet, raceFor, raceSetup
	riddleFor, riddleHint, heatQuiet, raceFor, raceSetup = riddle, hint, quiet, race, setup
	return func() { riddleFor, riddleHint, heatQuiet, raceFor, raceSetup = r, h, q, rc, s }
}
