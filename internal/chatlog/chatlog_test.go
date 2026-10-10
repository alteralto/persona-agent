package chatlog

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		line string
		want Message
		ok   bool
	}{
		{"<Steve> привет, пыхтун", Message{"Steve", "привет, пыхтун"}, true},
		{"§7[Игрок] §falteralto§7: иди за мной", Message{"alteralto", "иди за мной"}, true},
		{"[VIP] [Clan] Mage_01 » нужны ракеты", Message{"Mage_01", "нужны ракеты"}, true},
		{"Steve -> hi", Message{"Steve", "hi"}, true},
		{"Steve joined the game", Message{}, false},
		{"<Steve>   ", Message{}, false},
		{"", Message{}, false},
		{"§2§l§o[AutoClef] §rNew kill: BaDumTss -> Barrrrracuda", Message{}, false},
		{"§2§l§o[Tungsten] §rMovementQueue: off path (3,1) at -484,25 — replanning from here", Message{}, false},
		{"WARNING: Failed! No block path", Message{}, false},
	}
	for _, c := range cases {
		got, ok := Parse(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v, %v", c.line, got, ok, c.want, c.ok)
		}
	}
}

func TestNewLines(t *testing.T) {
	cases := []struct {
		name      string
		prev, cur []string
		want      []string
	}{
		{"nothing new", []string{"a", "b"}, []string{"a", "b"}, []string{}},
		{"appended", []string{"a", "b"}, []string{"a", "b", "c"}, []string{"c"}},
		{"ring rolled", []string{"a", "b", "c"}, []string{"b", "c", "d", "e"}, []string{"d", "e"}},
		{"no overlap", []string{"a"}, []string{"x", "y"}, []string{"x", "y"}},
		{"first poll", nil, []string{"a"}, []string{"a"}},
		{"cleared", []string{"a", "b"}, []string{}, []string{}},
	}
	for _, c := range cases {
		got := NewLines(c.prev, c.cur)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
