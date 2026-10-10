package agent

import "testing"

func TestMostlyNumbers(t *testing.T) {
	for text, want := range map[string]bool{
		"№ 11 Участник #64444063 10 баллов Бонус: 0": true,
		"12 34 56":          true,
		"Ферма тыкв":        false,
		"Иди в розовый дом": false,
		"Дом 7, вход слева": false,
	} {
		if got := mostlyNumbers(text); got != want {
			t.Errorf("mostlyNumbers(%q) = %v, want %v", text, got, want)
		}
	}
}
