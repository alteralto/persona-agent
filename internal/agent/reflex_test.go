package agent

import "testing"

func TestStripFormatting(t *testing.T) {
	if got := stripFormatting("§2§l§o[Tungsten] §rPunking player: pro228"); got != "[Tungsten] Punking player: pro228" {
		t.Fatalf("got %q", got)
	}
}
