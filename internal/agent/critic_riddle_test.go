package agent

import "testing"

func TestRiddleNamesItsPlace(t *testing.T) {
	stems := placeStems("жёлтое кольцо")
	for _, tc := range []struct {
		text  string
		named bool
	}{
		{"Очередная задачка, значит. Найдите жёлтое кольцо восточнее отсюда.", true},
		{"Ищите в кольце на востоке", true},
		{"Там, где солнце садится, висит обруч цвета одуванчика", false},
		{"Желтая штука на востоке", true},
	} {
		if _, got := namesPlace(tc.text, stems); got != tc.named {
			t.Errorf("namesPlace(%q) = %v, want %v", tc.text, got, tc.named)
		}
	}
	if s := placeStems("дом у озера"); len(s) != 1 || s[0] != "озер" {
		t.Errorf("short words are not stems: %v", s)
	}
}
