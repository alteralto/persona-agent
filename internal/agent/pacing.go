package agent

import "time"

// Speech pacing. Say only queues a line; the voice service plays the queue in order. A fixed
// 5 s between replies kept players waiting 4.8 s (median) on SkyBars (21.11) while the pipeline
// itself took 1.2 s, and the bot looked deaf. Now the next line waits for the one before it to
// finish, plus a short breath, and ReplyEvery is only the floor.
const (
	ttsFirstSound = 400 * time.Millisecond // synthesis before the first sound (median 334 ms on 21.11)
	speechPerRune = 58 * time.Millisecond  // Piper dmitri: about 17 characters a second
	speechBreath  = 300 * time.Millisecond // gap between two lines
)

// speechEnd: when a line queued at now will have been played, after whatever is queued before it.
func speechEnd(prevEnd, now time.Time, text string) time.Time {
	start := now
	if prevEnd.After(start) {
		start = prevEnd
	}
	return start.Add(ttsFirstSound + time.Duration(len([]rune(text)))*speechPerRune)
}

// replyWait: how long to hold the next line. The next line has its own synthesis delay, so it is
// sent that much before the queue runs dry.
func (a *Agent) replyWait(now time.Time) time.Duration {
	wait := a.cfg.ReplyEvery - now.Sub(a.lastReply)
	if a.speaker != nil {
		if w := a.speechEnds.Add(speechBreath - ttsFirstSound).Sub(now); w > wait {
			wait = w
		}
	}
	return wait
}
