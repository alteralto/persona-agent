package agent

import "time"

// ShortLinger shortens the stay after each activity; the returned func puts it back.
func ShortLinger(d time.Duration) func() {
	was := activityLinger
	activityLinger = d
	return func() { activityLinger = was }
}

// ShortGames makes the games' timings fit a test; the returned func puts them back.
func ShortGames(riddle, hint, quiet, race, setup time.Duration) func() {
	r, h, q, rc, s := riddleFor, riddleHint, heatQuiet, raceFor, raceSetup
	riddleFor, riddleHint, heatQuiet, raceFor, raceSetup = riddle, hint, quiet, race, setup
	return func() { riddleFor, riddleHint, heatQuiet, raceFor, raceSetup = r, h, q, rc, s }
}
