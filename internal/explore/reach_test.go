package explore

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// flat lays stone at y=64 over the whole cube around (0, 65, 0), plus extra blocks.
func flat(extra map[[3]int]string) Terrain {
	t := Terrain{Blocks: map[[3]int]string{}, Center: [3]int{0, 65, 0}, Radius: 5}
	for x := -5; x <= 5; x++ {
		for z := -5; z <= 5; z++ {
			t.Blocks[[3]int{x, 64, z}] = "камень"
		}
	}
	for c, b := range extra {
		t.Blocks[c] = b
	}
	return t
}

func TestReachableStopsAtAFence(t *testing.T) {
	fence := map[[3]int]string{}
	for z := -5; z <= 5; z++ {
		fence[[3]int{3, 65, z}] = "дубовый забор"
	}
	tr := flat(fence)
	reach := tr.Reachable([3]int{0, 65, 0}, nil)
	for c := range reach {
		if c[0] >= 3 {
			t.Fatalf("walked through the fence to %v", c)
		}
	}
	_, progress, ok := Stride(reach, [3]int{0, 65, 0}, 1, 0, nil)
	if !ok || progress > 2 {
		t.Fatalf("stride east: ok=%v progress=%.1f, want at most 2 (the fence is at x=3)", ok, progress)
	}
}

func TestReachableFindsTheGap(t *testing.T) {
	fence := map[[3]int]string{}
	for z := -5; z <= 5; z++ {
		if z != 4 {
			fence[[3]int{3, 65, z}] = "булыжная стена"
		}
	}
	tr := flat(fence)
	reach := tr.Reachable([3]int{0, 65, 0}, nil)
	if _, ok := reach[[3]int{5, 65, 0}]; !ok {
		t.Fatal("did not find the way round through the gap at z=4")
	}
}

func TestReachableStepsUpAndDropsDown(t *testing.T) {
	tr := flat(map[[3]int]string{
		{2, 65, 0}:  "каменные кирпичи", // a one-block step up
		{2, 65, 1}:  "каменные кирпичи",
		{-2, 64, 0}: "", // replaced below: a hole two deep
	})
	delete(tr.Blocks, [3]int{-2, 64, 0})
	tr.Blocks[[3]int{-2, 62, 0}] = "камень"
	reach := tr.Reachable([3]int{0, 65, 0}, nil)
	if _, ok := reach[[3]int{2, 66, 0}]; !ok {
		t.Error("no step up onto the block")
	}
	if _, ok := reach[[3]int{-2, 63, 0}]; !ok {
		t.Error("no drop into the hole")
	}
}

func TestReachableKeepsOutOfWaterAndPortals(t *testing.T) {
	tr := flat(map[[3]int]string{
		{1, 64, 0}:  "вода",
		{0, 65, 1}:  "портал в незер",
		{-1, 65, 0}: "высокая трава",
	})
	reach := tr.Reachable([3]int{0, 65, 0}, nil)
	if _, ok := reach[[3]int{1, 65, 0}]; ok {
		t.Error("stood on water")
	}
	if _, ok := reach[[3]int{0, 65, 1}]; ok {
		t.Error("walked into a portal")
	}
	if _, ok := reach[[3]int{-1, 65, 0}]; !ok {
		t.Error("tall grass should not stop a walk")
	}
}

func TestReachableGoesAroundPlayers(t *testing.T) {
	tr := flat(nil)
	occupied := map[[2]int]bool{{1, 0}: true}
	reach := tr.Reachable([3]int{0, 65, 0}, occupied)
	if _, ok := reach[[3]int{1, 65, 0}]; ok {
		t.Error("walked into a standing player")
	}
	if _, ok := reach[[3]int{2, 65, 0}]; !ok {
		t.Error("did not go round the player")
	}
}

func TestStrideAvoidsWalkedCells(t *testing.T) {
	tr := flat(nil)
	reach := tr.Reachable([3]int{0, 65, 0}, nil)
	walkedEast := func(x, z int) int {
		if x > 0 {
			return 3
		}
		return 0
	}
	c, _, ok := Stride(reach, [3]int{0, 65, 0}, 0, 0, walkedEast)
	if !ok || c[0] > 0 {
		t.Fatalf("with no heading the stride should leave the walked east side, got %v ok=%v", c, ok)
	}
}

// A real cube from the SkyBars lobby (21.11): the walker must find ground to go to.
func TestReachableOnARealLobby(t *testing.T) {
	raw, err := os.ReadFile("testdata/skybars-lobby-blocks5.json")
	if err != nil {
		t.Skip(err)
	}
	var check []map[string]string
	if json.Unmarshal(raw, &check) != nil {
		t.Fatal("bad test data")
	}
	tr, ok := ParseTerrain(string(raw), [3]int{0, 65, 26}, 5)
	if !ok {
		t.Fatal("parse")
	}
	reach := tr.Reachable([3]int{0, 65, 26}, nil)
	c, _, ok := Stride(reach, [3]int{0, 65, 26}, 0, 1, nil)
	if !ok {
		t.Fatalf("no stride on the lobby floor; reached %d cells", len(reach))
	}
	if !tr.Standable(c) {
		t.Fatalf("stride target %v is not standable", c)
	}
	fmt.Println("lobby: reached", len(reach), "cells, stride to", c)
}
